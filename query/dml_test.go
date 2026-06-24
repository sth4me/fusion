package query

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"fusion/col"
	"fusion/dialect"
	"fusion/meta"
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
