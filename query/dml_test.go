package query

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/meta"
)

// dmlTestModel 测试模型
type dmlTestModel struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func regDMLModel() *meta.Table[dmlTestModel] {
	return meta.Register[dmlTestModel]("dml_users")
}

// fakeDMLExecer 捕获 SQL/参数
type fakeDMLExecer struct {
	lastSQL  string
	lastArgs []any
	execErr  error
}

func (f *fakeDMLExecer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("not impl")
}
func (f *fakeDMLExecer) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (f *fakeDMLExecer) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	f.lastSQL = q
	f.lastArgs = args
	return fakeResult{}, f.execErr
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 1, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

// TestInsertSQLGeneration 验证 Insert 生成的 SQL（MySQL 无 RETURNING 路径）
func TestInsertSQLGeneration(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}
	u.Name.Set("alice")
	u.Age.Set(30)

	err := NewInsert(tab, dialect.MySQLDialect, fe, u).Exec(context.Background())
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// MySQL 无 RETURNING，走 LastInsertId
	if !strings.Contains(fe.lastSQL, "INSERT INTO `dml_users`") {
		t.Errorf("SQL got %q", fe.lastSQL)
	}
	if strings.Contains(fe.lastSQL, "RETURNING") {
		t.Errorf("MySQL should not have RETURNING: %q", fe.lastSQL)
	}
}

// TestInsertUpsertMySQL 验证 OnConflict 生成 UPSERT 子句（MySQL 无 RETURNING 路径）
func TestInsertUpsertMySQL(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}
	u.ID.Set(1)
	u.Name.Set("alice")
	u.Age.Set(30)

	err := NewInsert(tab, dialect.MySQLDialect, fe, u).
		OnConflict([]string{"id"}, []string{"name", "age"}).
		Exec(context.Background())
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !strings.Contains(fe.lastSQL, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("SQL should have UPSERT: %q", fe.lastSQL)
	}
}

// TestUpdateSQLGeneration 验证局部更新 SQL（只更新 set 字段）
func TestUpdateSQLGeneration(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}
	u.Age.Set(25) // 只 Set Age

	err := NewUpdate(tab, dialect.PostgresDialect, fe, u).
		Where(tab.Proto.ID.Eq(1)).
		Exec(context.Background())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// 提取 SET 子句（SET 与 WHERE 之间），只检查 SET 部分
	sqlStr := fe.lastSQL
	setPart := sqlStr
	if i := strings.Index(sqlStr, " WHERE "); i >= 0 {
		setPart = sqlStr[:i]
	}
	if !strings.Contains(setPart, `"age" = `) {
		t.Errorf("SET should update age: %q", setPart)
	}
	if strings.Contains(setPart, `"name" =`) {
		t.Errorf("SET should NOT update name (not set): %q", setPart)
	}
	if strings.Contains(setPart, `"id" =`) {
		t.Errorf("SET should NOT update id (primary key): %q", setPart)
	}
}

// TestUpdateAllFields 验证 AllFields 强制全字段
func TestUpdateAllFields(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}

	err := NewUpdate(tab, dialect.PostgresDialect, fe, u).
		Where(tab.Proto.ID.Eq(1)).
		AllFields().
		Exec(context.Background())
	if err != nil {
		t.Fatalf("update all: %v", err)
	}
	// 全字段（除主键 id）
	for _, colName := range []string{`"name" =`, `"age" =`, `"email" =`} {
		if !strings.Contains(fe.lastSQL, colName) {
			t.Errorf("AllFields should update %s: %q", colName, fe.lastSQL)
		}
	}
}

// TestUpdateResetThenAllFields 验证 Col.Reset() 撤销 dirty 后，
// 普通 Update 只发该字段；AllFields 仍可强制全量。
func TestUpdateResetThenAllFields(t *testing.T) {
	tab := regDMLModel()

	// 1) Set 过 Age，再 Reset → 普通 Update 应报"无字段"（dirty 已清）
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}
	u.Age.Set(25)
	u.Age.Reset()
	u.ID.Set(1) // 主键用于 Where
	err := NewUpdate(tab, dialect.PostgresDialect, fe, u).
		Where(tab.Proto.ID.Eq(1)).
		Exec(context.Background())
	if err == nil {
		t.Error("after Reset, update with no other set field should error")
	}

	// 2) 同一个对象 AllFields 仍能强制全量更新
	fe2 := &fakeDMLExecer{}
	err = NewUpdate(tab, dialect.PostgresDialect, fe2, u).
		Where(tab.Proto.ID.Eq(1)).
		AllFields().
		Exec(context.Background())
	if err != nil {
		t.Fatalf("AllFields after Reset should succeed: %v", err)
	}
	for _, colName := range []string{`"name" =`, `"age" =`, `"email" =`} {
		if !strings.Contains(fe2.lastSQL, colName) {
			t.Errorf("AllFields should update %s: %q", colName, fe2.lastSQL)
		}
	}
}

// TestUpdateNoFieldsError 验证无 set 字段时报错
func TestUpdateNoFieldsError(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{} // 啥都没 Set

	err := NewUpdate(tab, dialect.PostgresDialect, fe, u).
		Where(tab.Proto.ID.Eq(1)).
		Exec(context.Background())
	if err == nil {
		t.Error("update with no fields should error")
	}
}

// TestDeleteSQLGeneration 验证 Delete SQL
func TestDeleteSQLGeneration(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}

	err := NewDelete(tab, dialect.PostgresDialect, fe).
		Where(tab.Proto.Name.Eq("alice")).
		Exec(context.Background())
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := `DELETE FROM "dml_users" WHERE "name" = $1`
	if fe.lastSQL != want {
		t.Errorf("got %q, want %q", fe.lastSQL, want)
	}
}

// compositePKModel 复合主键测试模型（user_id + role_id 都是 db:"pk"）。
type compositePKModel struct {
	UserID col.Col[int64] `db:"pk"`
	RoleID col.Col[int64] `db:"pk"`
	Name   col.Col[string]
}

func regCompositePKModel() *meta.Table[compositePKModel] {
	return meta.Register[compositePKModel]("dml_user_roles")
}

// TestCompositePK_BuildPKWhere 复合主键 Update 自动构造 WHERE（pk1=? AND pk2=?）。
func TestCompositePK_BuildPKWhere(t *testing.T) {
	tab := regCompositePKModel()
	fe := &fakeDMLExecer{}
	u := &compositePKModel{}
	u.UserID.Set(1)
	u.RoleID.Set(2)
	u.Name.Set("admin") // 改 Name

	err := NewUpdate(tab, dialect.PostgresDialect, fe, u). // 无 Where → 自动按复合 PK
										Exec(context.Background())
	if err != nil {
		t.Fatalf("update composite PK: %v", err)
	}
	// WHERE 应含两个 PK 条件，用 AND 连接
	if !strings.Contains(fe.lastSQL, `"user_id" = $`) || !strings.Contains(fe.lastSQL, `"role_id" = $`) {
		t.Errorf("WHERE should contain both PK cols: %q", fe.lastSQL)
	}
	if !strings.Contains(fe.lastSQL, " AND ") {
		t.Errorf("WHERE should use AND for composite PK: %q", fe.lastSQL)
	}
	// SET 只应有 name（PK 列不参与 SET）
	setPart := fe.lastSQL
	if i := strings.Index(fe.lastSQL, " WHERE "); i >= 0 {
		setPart = fe.lastSQL[:i]
	}
	if !strings.Contains(setPart, `"name" =`) {
		t.Errorf("SET should update name: %q", setPart)
	}
	if strings.Contains(setPart, `"user_id" =`) || strings.Contains(setPart, `"role_id" =`) {
		t.Errorf("SET should NOT update PK cols: %q", setPart)
	}
}

// TestCompositePK_DeleteByIDs 复合主键删除用 DeleteByIDs(map)。
func TestCompositePK_DeleteByIDs(t *testing.T) {
	tab := regCompositePKModel()
	fe := &fakeDMLExecer{}
	err := NewDeleteByID(tab, dialect.PostgresDialect, fe,
		map[string]any{"user_id": int64(1), "role_id": int64(2)}).
		Exec(context.Background())
	if err != nil {
		t.Fatalf("delete by composite PK: %v", err)
	}
	if !strings.Contains(fe.lastSQL, `"user_id" = $`) || !strings.Contains(fe.lastSQL, `"role_id" = $`) {
		t.Errorf("delete WHERE should contain both PK cols: %q", fe.lastSQL)
	}
}

// TestInsertNullField 验证 NULL 字段插入（Col[*string] nil）
func TestInsertNullField(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{}
	u := &dmlTestModel{}
	u.Name.Set("alice")
	u.Age.Set(30)
	// Email 不 Set（保持 nil），但 AllFields 场景会包含
	// 这里 Insert 只插 set 的，Email 未 set 不插入

	err := NewInsert(tab, dialect.MySQLDialect, fe, u).Exec(context.Background())
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 应不含 email（未 set）
	if strings.Contains(fe.lastSQL, "`email`") {
		t.Errorf("unset email should not be in INSERT: %q", fe.lastSQL)
	}
}

// TestExecErrorPropagation 验证 Exec 错误传播
func TestExecErrorPropagation(t *testing.T) {
	tab := regDMLModel()
	fe := &fakeDMLExecer{execErr: errors.New("db down")}
	u := &dmlTestModel{}
	u.Name.Set("alice")

	err := NewUpdate(tab, dialect.MySQLDialect, fe, u).
		Where(tab.Proto.ID.Eq(1)).
		Exec(context.Background())
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("should propagate exec error, got %v", err)
	}
}

// TestIsPrimaryKey 验证主键判断
func TestIsPrimaryKey(t *testing.T) {
	tab := regDMLModel()
	if !isPrimaryKey(tab.Meta, "id") {
		t.Error("id should be primary key")
	}
	if isPrimaryKey(tab.Meta, "name") {
		t.Error("name should not be primary key")
	}
}

// TestReturningCols 验证主键列返回
func TestReturningCols(t *testing.T) {
	tab := regDMLModel()
	i := &Inserter[dmlTestModel]{table: tab}
	cols := i.returningCols()
	if len(cols) != 1 || cols[0] != "id" {
		t.Errorf("returningCols got %v, want [id]", cols)
	}
}
