package fusion_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
)

// FUser 功能测试模型
type FUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func setupFeaturesDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db := openSQLite(t)
	execSQL(db, `CREATE TABLE fusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER, email TEXT)`)
	execSQL(db, `INSERT INTO fusers VALUES (1,'alice',30,'a@e.com'),(2,'bob',17,NULL),(3,'carol',25,'c@e.com')`)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	fusion.Register[FUser]("fusers")
	return fusion.WrapDB(db), db
}

// TestLike Like 模糊查询
func TestLike(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	// name LIKE 'a%' → alice
	users, err := fusion.From(Users, wrapped).
		Where(Users.Proto.Name.Like("a%")).
		All(context.Background())
	if err != nil {
		t.Fatalf("like: %v", err)
	}
	if len(users) != 1 || users[0].Name.Get() != "alice" {
		t.Errorf("got %d users, want 1 (alice)", len(users))
	}
}

// TestNotLike NotLike
func TestNotLike(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	users, _ := fusion.From(Users, wrapped).
		Where(Users.Proto.Name.NotLike("a%")).
		All(context.Background())
	// bob, carol 不以 a 开头
	if len(users) != 2 {
		t.Errorf("got %d, want 2", len(users))
	}
}

// TestBetween Between 区间
func TestBetween(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	// age BETWEEN 18 AND 30 → alice(30), carol(25)
	users, _ := fusion.From(Users, wrapped).
		Where(Users.Proto.Age.Between(18, 30)).
		All(context.Background())
	if len(users) != 2 {
		t.Errorf("got %d, want 2", len(users))
	}
	for _, u := range users {
		if u.Age.Get() < 18 || u.Age.Get() > 30 {
			t.Errorf("age %d not in [18,30]", u.Age.Get())
		}
	}
}

// TestInsertBatch 批量插入 + RETURNING 回填主键
func TestInsertBatch(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	u1 := &FUser{}
	u1.Name.Set("dave")
	u1.Age.Set(40)
	u2 := &FUser{}
	u2.Name.Set("eve")
	u2.Age.Set(50)
	u2.Email.Set((*string)(nil)) // 显式 NULL

	err := fusion.InsertBatch(Users, wrapped, []*FUser{u1, u2}).Exec(context.Background())
	if err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	// 主键应回填
	if u1.ID.Get() == 0 || u2.ID.Get() == 0 {
		t.Error("batch insert should backfill IDs via RETURNING")
	}

	// 验证插入
	all, _ := fusion.From(Users, wrapped).All(context.Background())
	if len(all) != 5 { // 3 原有 + 2 新
		t.Errorf("got %d users, want 5", len(all))
	}
}

// TestUpdateByPK 主键便捷 Update（无 Where 自动按主键）
func TestUpdateByPK(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	// 查出 alice，改 Age，无 Where 自动按主键更新
	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	got.Age.Set(99)
	err := fusion.Update(Users, wrapped, &got).Exec(context.Background())
	if err != nil {
		t.Fatalf("update by pk: %v", err)
	}
	after, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	if after.Age.Get() != 99 {
		t.Errorf("age got %d, want 99", after.Age.Get())
	}
}

// TestUpsert Upsert（ON CONFLICT）
func TestUpsert(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	// 插入 id=1 冲突时更新 name/age
	u := &FUser{}
	u.ID.Set(1)
	u.Name.Set("alice2")
	u.Age.Set(31)
	err := fusion.Upsert(Users, wrapped, u).
		OnConflict([]string{"id"}, []string{"name", "age"}).
		Exec(context.Background())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	after, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	if after.Name.Get() != "alice2" || after.Age.Get() != 31 {
		t.Errorf("upsert result got name=%s age=%d", after.Name.Get(), after.Age.Get())
	}
}

// TestErrNotFound 哨兵错误 ErrNotFound
func TestErrNotFound(t *testing.T) {
	wrapped, raw := setupFeaturesDB(t)
	defer raw.Close()
	Users := fusion.Register[FUser]("fusers")

	_, err := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(99999)).One(context.Background())
	if err == nil {
		t.Fatal("should error for not found")
	}
	// errors.Is(err, fusion.ErrNotFound) 应为 true
	if !errors.Is(err, fusion.ErrNotFound) {
		t.Errorf("should be ErrNotFound, got %v", err)
	}
	// 同时兼容 sql.ErrNoRows
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("should also be sql.ErrNoRows, got %v", err)
	}
}
