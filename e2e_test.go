package fusion_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

	// 纯 Go SQLite 驱动（无 CGO）
	_ "modernc.org/sqlite"
)

// User 是全包装模型：所有字段都是 col.Col[T]。
type User struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string] // 可空字段，nil = NULL（见 #3）
}

// openSQLite 打开内存 SQLite 数据库并建表。
// SQLite 的 :memory: 每个连接是独立实例，故 SetMaxOpenConns(1) 确保所有查询
// （含 Preload 子查询）共用同一实例。
func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	// 建表（手工 DDL，阶段0不做迁移）
	ddl := `CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		age INTEGER NOT NULL,
		email TEXT
	)`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func seedUsers(t *testing.T, db *sql.DB) {
	t.Helper()
	rows := []struct {
		id    int64
		name  string
		age   int
		email *string
	}{
		{1, "alice", 30, strPtr("alice@example.com")},
		{2, "bob", 17, nil},
		{3, "carol", 25, strPtr("carol@example.com")},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO users (id, name, age, email) VALUES (?, ?, ?, ?)`,
			r.id, r.name, r.age, r.email,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func strPtr(s string) *string { return &s }

// TestE2E_SelectAll 验证：注册模型 → From → All 全流程
func TestE2E_SelectAll(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	users, err := fusion.From(Users, db).All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("got %d users, want 3", len(users))
	}
	// 验证字段扫描正确
	if users[0].Name.Get() != "alice" {
		t.Errorf("first user name got %q, want alice", users[0].Name.Get())
	}
	if users[0].Age.Get() != 30 {
		t.Errorf("alice age got %d, want 30", users[0].Age.Get())
	}
	// 可空字段：alice 有 email，bob 无（nil）
	if users[0].Email.Get() == nil || *users[0].Email.Get() != "alice@example.com" {
		t.Errorf("alice email got %v", users[0].Email.Get())
	}
	if users[1].Email.Get() != nil {
		t.Errorf("bob email should be nil, got %v", users[1].Email.Get())
	}
}

// TestE2E_WhereExpr 验证类型安全的 WHERE 表达式 + 局部扫描
func TestE2E_WhereExpr(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	// 查询 age > 18（用 Col.Gt，类型安全）
	users, err := fusion.From(Users, db).
		Where(Users.Proto.Age.Gt(18)).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// alice(30) 和 carol(25) 满足，bob(17) 不满足
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
}

// TestE2E_WhereComplex 验证复杂表达式（And/Or 跨类括号）
func TestE2E_WhereComplex(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	// (name = 'alice' AND age > 18) OR name = 'bob'
	u := Users.Proto
	users, err := fusion.From(Users, db).
		Where(
			u.Name.Eq("alice").And(u.Age.Gt(18)).
				Or(u.Name.Eq("bob")),
		).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// alice(满足 AND 分支) + bob(满足 OR 分支)
	if len(users) != 2 {
		names := []string{}
		for _, x := range users {
			names = append(names, x.Name.Get())
		}
		t.Fatalf("got %d users (%v), want 2 (alice, bob)", len(users), names)
	}
}

// TestE2E_OrderByLimit 验证排序 + LIMIT
func TestE2E_OrderByLimit(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	users, err := fusion.From(Users, db).
		OrderBy(Users.Proto.Age.Desc()).
		Limit(2).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d, want 2", len(users))
	}
	// 按 age 降序：alice(30) 在前，carol(25) 次之
	if users[0].Name.Get() != "alice" {
		t.Errorf("first got %q, want alice", users[0].Name.Get())
	}
	if users[1].Name.Get() != "carol" {
		t.Errorf("second got %q, want carol", users[1].Name.Get())
	}
}

// TestE2E_One 验证 One（自动 LIMIT 1）+ ErrNoRows
func TestE2E_One(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	u, err := fusion.From(Users, db).
		Where(Users.Proto.Name.Eq("alice")).
		One(context.Background())
	if err != nil {
		t.Fatalf("one: %v", err)
	}
	if u.Name.Get() != "alice" {
		t.Errorf("got %q", u.Name.Get())
	}

	// 不存在的记录（One 直接返回 sql.ErrNoRows）
	_, err = fusion.From(Users, db).Where(Users.Proto.Name.Eq("zzz")).One(context.Background())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("got %v, want sql.ErrNoRows", err)
	}
}

// TestE2E_Raw 验证 Raw SQL 兜底
func TestE2E_Raw(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)

	var users []User
	err := fusion.Raw[User](&users, context.Background(), db,
		`SELECT id, name, age, email FROM users WHERE age > ?`, 20)
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d, want 2 (alice, carol)", len(users))
	}
}

// TestE2E_JSONTransparent 验证 JSON 透明序列化贯穿 ORM
func TestE2E_JSONTransparent(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	seedUsers(t, db)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[User]("users")

	users, _ := fusion.From(Users, db).All(context.Background())
	alice := users[0]
	b, err := jsonMarshal(alice)
	if err != nil {
		t.Fatal(err)
	}
	// 应输出原生形态
	got := string(b)
	if !contains(got, `"Name":"alice"`) {
		t.Errorf("json missing Name:alice, got %s", got)
	}
	if !contains(got, `"Age":30`) {
		t.Errorf("json missing Age:30, got %s", got)
	}
}
