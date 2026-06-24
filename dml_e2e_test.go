package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
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
