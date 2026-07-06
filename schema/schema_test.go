package schema

import (
	"context"
	"database/sql"
	"testing"

	"fusion/dialect"

	_ "modernc.org/sqlite"
)

func openSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// users: 单列主键 + 索引；posts: 外键引用 users + 复合索引
	ddls := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`,
		`CREATE UNIQUE INDEX idx_users_email ON users(email)`,
		`CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, title TEXT NOT NULL, slug TEXT, FOREIGN KEY (user_id) REFERENCES users(id))`,
		`CREATE INDEX idx_posts_user_slug ON posts(user_id, slug)`,
	}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("exec %q: %v", ddl, err)
		}
	}
	return db
}

func TestLoadSQLite_ListTables(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	ctx := context.Background()

	cat, err := Load(ctx, db, dialect.SQLiteDialect)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := cat.Tables()
	want := map[string]bool{"users": true, "posts": true}
	if len(names) != 2 {
		t.Fatalf("got %d tables %v, want 2", len(names), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected table %q", n)
		}
	}
}

func TestLoadSQLite_DescribeUsers(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	ctx := context.Background()

	cat, err := Load(ctx, db, dialect.SQLiteDialect, "users")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tab := cat.Table("users")
	if tab == nil {
		t.Fatal("users table nil")
	}
	// 列：id, name, email
	if len(tab.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(tab.Columns))
	}
	id := tab.Column("id")
	if id == nil {
		t.Fatal("id column nil")
	}
	if id.SQLType != "INTEGER" {
		t.Errorf("id SQLType got %q, want INTEGER", id.SQLType)
	}
	name := tab.Column("name")
	if name.Nullable {
		t.Error("name should be NOT NULL")
	}
	if !tab.Column("email").Nullable {
		t.Error("email should be nullable")
	}
	// 主键
	if len(tab.PrimaryKey) != 1 || tab.PrimaryKey[0] != "id" {
		t.Errorf("PK got %v, want [id]", tab.PrimaryKey)
	}
	// 索引：idx_users_email 唯一
	var emailIdx *Index
	for i := range tab.Indexes {
		if tab.Indexes[i].Name == "idx_users_email" {
			emailIdx = &tab.Indexes[i]
		}
	}
	if emailIdx == nil {
		t.Fatal("idx_users_email not found")
	}
	if !emailIdx.Unique {
		t.Error("idx_users_email should be unique")
	}
	if len(emailIdx.Columns) != 1 || emailIdx.Columns[0] != "email" {
		t.Errorf("idx_users_email cols got %v", emailIdx.Columns)
	}
	// users 无外键
	if len(tab.ForeignKeys) != 0 {
		t.Errorf("users should have no FK, got %d", len(tab.ForeignKeys))
	}
}

func TestLoadSQLite_DescribePostsWithFK(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	ctx := context.Background()

	cat, err := Load(ctx, db, dialect.SQLiteDialect, "posts")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tab := cat.Table("posts")
	if tab == nil {
		t.Fatal("posts table nil")
	}
	// 外键：posts.user_id → users.id
	if len(tab.ForeignKeys) != 1 {
		t.Fatalf("got %d FKs, want 1", len(tab.ForeignKeys))
	}
	fk := tab.ForeignKeys[0]
	if fk.Column != "user_id" {
		t.Errorf("FK column got %q, want user_id", fk.Column)
	}
	if fk.RefTable != "users" {
		t.Errorf("FK ref table got %q, want users", fk.RefTable)
	}
	if len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Errorf("FK ref cols got %v, want [id]", fk.RefColumns)
	}
	// 复合索引 idx_posts_user_slug（非唯一）
	var slugIdx *Index
	for i := range tab.Indexes {
		if tab.Indexes[i].Name == "idx_posts_user_slug" {
			slugIdx = &tab.Indexes[i]
		}
	}
	if slugIdx == nil {
		t.Fatal("idx_posts_user_slug not found")
	}
	if slugIdx.Unique {
		t.Error("idx_posts_user_slug should NOT be unique")
	}
	if len(slugIdx.Columns) != 2 || slugIdx.Columns[0] != "user_id" || slugIdx.Columns[1] != "slug" {
		t.Errorf("idx_posts_user_slug cols got %v, want [user_id slug]", slugIdx.Columns)
	}
}

func TestLoadSQLite_CompositePK(t *testing.T) {
	// 复合主键表
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE user_roles (user_id INTEGER, role_id INTEGER, PRIMARY KEY (user_id, role_id))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	ctx := context.Background()
	cat, err := Load(ctx, db, dialect.SQLiteDialect, "user_roles")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tab := cat.Table("user_roles")
	if len(tab.PrimaryKey) != 2 {
		t.Fatalf("PK got %v, want 2 cols", tab.PrimaryKey)
	}
	if tab.PrimaryKey[0] != "user_id" || tab.PrimaryKey[1] != "role_id" {
		t.Errorf("PK order got %v, want [user_id role_id]", tab.PrimaryKey)
	}
}

func TestIntrospectorFor_UnknownDialect(t *testing.T) {
	unknown := unknownDialect{}
	_, err := IntrospectorFor(unknown)
	if err == nil {
		t.Error("expected error for unknown dialect")
	}
}

func TestIntrospectorFor_AllKnown(t *testing.T) {
	// 三方言都应返回对应 Introspector（不报错）
	for _, d := range []dialect.Dialect{dialect.SQLiteDialect, dialect.PostgresDialect, dialect.MySQLDialect} {
		if _, err := IntrospectorFor(d); err != nil {
			t.Errorf("IntrospectorFor(%s): %v", d.Name(), err)
		}
	}
}

func TestParsePgArray(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"{a,b,c}", []string{"a", "b", "c"}},
		{"{id}", []string{"id"}},
		{"{}", nil},
		{"", nil},
		{"NULL", nil},
		{"user_id", []string{"user_id"}},
	}
	for _, c := range cases {
		got := parsePgArray(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parsePgArray(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parsePgArray(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// unknownDialect 仅用于测试 IntrospectorFor 的未知分支。
type unknownDialect struct{}

func (unknownDialect) Name() string                                { return "oracle" }
func (unknownDialect) Placeholder(int) string                      { return "?" }
func (unknownDialect) QuoteIdent(string) string                    { return `"` + `"` }
func (d unknownDialect) QuoteTable(n string) string                { return d.QuoteIdent(n) }
func (unknownDialect) SupportsReturning() bool                     { return false }
func (unknownDialect) UpsertOnConflict(_, _ []string) string       { return "" }
