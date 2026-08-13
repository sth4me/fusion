package fusion_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// DMLUser 用于 DML 测试的模型
type DMLUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func openSQLiteForDML(t *testing.T) *sql.DB {
	return openSQLite(t)
}

func TestDML_InsertReturning(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	// Insert：设置非主键字段，主键应通过 RETURNING 回填
	u := &DMLUser{}
	u.Name.Set("dave")
	u.Age.Set(40)
	u.Email.Set(strPtr("dave@e.com"))

	if err := fusion.Insert(Users, db, u).Exec(context.Background()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// SQLite 支持 RETURNING，主键应回填
	if u.ID.Get() == 0 {
		t.Error("ID should be backfilled via RETURNING")
	}

	// 验证确实写进去了
	got, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(u.ID.Get())).One(context.Background())
	if got.Name.Get() != "dave" {
		t.Errorf("got name %q", got.Name.Get())
	}
}

func TestDML_InsertNullField(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	u := &DMLUser{}
	u.Name.Set("eve")
	u.Age.Set(22)
	// Email 不 Set（保持 nil）
	if err := fusion.Insert(Users, db, u).Exec(context.Background()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := fusion.From(Users, db).Where(Users.Proto.Name.Eq("eve")).One(context.Background())
	if got.Email.Get() != nil {
		t.Error("Email should be NULL")
	}
}

func TestDML_UpdatePartial(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	// 预置数据
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (2,'bob',17,NULL)")

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	// 查出 alice，只改 Age
	got, _ := fusion.From(Users, db).Where(Users.Proto.Name.Eq("alice")).One(context.Background())
	got.Age.Set(31) // 只 Set 了 Age

	if err := fusion.Update(Users, db, &got).
		Where(Users.Proto.ID.Eq(got.ID.Get())).
		Exec(context.Background()); err != nil {
		t.Fatalf("update: %v", err)
	}

	// 验证：Age 改了，Name/Email 不变（局部更新）
	after, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(got.ID.Get())).One(context.Background())
	if after.Age.Get() != 31 {
		t.Errorf("age got %d, want 31", after.Age.Get())
	}
	if after.Name.Get() != "alice" {
		t.Errorf("name changed to %q (should stay alice)", after.Name.Get())
	}
	if after.Email.Get() == nil || *after.Email.Get() != "a@e.com" {
		t.Errorf("email changed (should stay a@e.com): %v", after.Email.Get())
	}
}

func TestDML_UpdateZeroValue(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	got, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	// 用 Set(0) 把 age 清零——应能更新（靠 set 标志，不靠值，见 #3）
	got.Age.Set(0)

	if err := fusion.Update(Users, db, &got).Where(Users.Proto.ID.Eq(1)).Exec(context.Background()); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	if after.Age.Get() != 0 {
		t.Errorf("age got %d, want 0 (zero value should update)", after.Age.Get())
	}
}

func TestDML_Delete(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (2,'bob',17,NULL)")
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	// 删除 bob
	if err := fusion.Delete(Users, db).Where(Users.Proto.Name.Eq("bob")).Exec(context.Background()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ := fusion.From(Users, db).All(context.Background())
	if len(all) != 1 {
		t.Errorf("got %d users, want 1 (bob deleted)", len(all))
	}
	if all[0].Name.Get() != "alice" {
		t.Errorf("remaining user got %q, want alice", all[0].Name.Get())
	}
}

func TestDML_UpdateAllFields(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	got, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	// AllFields：即使没 Set，也更新全部字段
	if err := fusion.Update(Users, db, &got).Where(Users.Proto.ID.Eq(1)).AllFields().Exec(context.Background()); err != nil {
		t.Fatalf("update all: %v", err)
	}
	// 应不报错且数据保持（因为值没变）
}

// TestDML_OnConflictDoNothing 验证"冲突即忽略"的幂等写入语义。
// 预置唯一键冲突：同一 name 第二次插入不报错、不覆盖原数据。
func TestDML_OnConflictDoNothing(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	// 用独立表：name 加 UNIQUE 约束制造冲突
	mustExecP(db, "DROP TABLE IF EXISTS users")
	mustExecP(db, `CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		age INTEGER NOT NULL,
		email TEXT
	)`)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	// 第一次插入：成功
	u1 := &DMLUser{}
	u1.Name.Set("dave")
	u1.Age.Set(40)
	u1.Email.Set(strPtr("dave@e.com"))
	if err := fusion.Insert(Users, db, u1).
		OnConflictDoNothing([]string{"name"}).
		Exec(context.Background()); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// 第二次插入同 name：不报错（DO NOTHING），不覆盖
	u2 := &DMLUser{}
	u2.Name.Set("dave")
	u2.Age.Set(99) // 与已存在行不同，若被覆盖则说明 DO NOTHING 失效
	u2.Email.Set(strPtr("hacker@e.com"))
	if err := fusion.Insert(Users, db, u2).
		OnConflictDoNothing([]string{"name"}).
		Exec(context.Background()); err != nil {
		t.Fatalf("second insert should be ignored, got: %v", err)
	}

	// 全表应只有 1 行，且是第一次插入的数据
	all, err := fusion.From(Users, db).All(context.Background())
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want 1", len(all))
	}
	if all[0].Name.Get() != "dave" || all[0].Age.Get() != 40 {
		t.Errorf("original row should be preserved, got %+v", all[0])
	}
	if all[0].Email.Get() == nil || *all[0].Email.Get() != "dave@e.com" {
		t.Errorf("email should stay original, got %v", all[0].Email.Get())
	}
}

// TestDML_OnConflictDoNothing_MySQL 验证 MySQL 方言下 Exec 报不支持错误。
func TestDML_OnConflictDoNothing_MySQL(t *testing.T) {
	db := openSQLite(t) // 物理上是 SQLite，但方言设为 MySQL 触发校验
	defer db.Close()
	fusion.SetDefaultDialect(dialect.MySQLDialect)
	Users := fusion.Register[DMLUser]("users")

	u := &DMLUser{}
	u.Name.Set("x")
	u.Age.Set(1)
	err := fusion.Insert(Users, db, u).
		OnConflictDoNothing([]string{"name"}).
		Exec(context.Background())
	if err == nil {
		t.Fatal("MySQL + DoNothing should return error")
	}
	if !strings.Contains(err.Error(), "不支持") {
		t.Errorf("error should mention MySQL unsupported, got: %v", err)
	}
}

// TestDML_UpdateExpectAffected 乐观锁行数校验：ExpectAffected(1) 时
// 版本冲突（rows=0）必须报错，不能静默成功；正常更新通过。
func TestDML_UpdateExpectAffected(t *testing.T) {
	db := openSQLiteForDML(t)
	defer db.Close()
	execInsert(db, "INSERT INTO users (id,name,age,email) VALUES (1,'eve',25,'e@e.com')")
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[DMLUser]("users")

	// 正常更新：恰好 1 行 → 通过
	got, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	got.Age.Set(26)
	if err := fusion.Update(Users, db, &got).
		Where(Users.Proto.ID.Eq(1)).ExpectAffected(1).
		Exec(context.Background()); err != nil {
		t.Fatalf("正常更新 ExpectAffected(1) 应通过: %v", err)
	}

	// 版本冲突模拟：WHERE 不匹配（模拟 version=旧值）→ rows=0 → 必须报错
	got2, _ := fusion.From(Users, db).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	got2.Age.Set(27)
	err := fusion.Update(Users, db, &got2).
		Where(Users.Proto.ID.Eq(1).And(Users.Proto.Age.Eq(999))). // 无匹配行
		ExpectAffected(1).
		Exec(context.Background())
	if err == nil {
		t.Fatal("ExpectAffected(1) 且 rows=0 应报错（乐观锁冲突不得静默）")
	}

	// 不设 ExpectAffected：rows=0 仍静默（默认兼容，不影响存量调用）
	if err := fusion.Update(Users, db, &got2).
		Where(Users.Proto.ID.Eq(1).And(Users.Proto.Age.Eq(999))).
		Exec(context.Background()); err != nil {
		t.Fatalf("默认（不校验）rows=0 应静默: %v", err)
	}
}
