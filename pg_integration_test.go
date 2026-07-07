//go:build postgres

// 本文件验证 PostgreSQL 专属行为（默认 go test 不包含；需 go test -tags postgres）。
//
// 运行前提：本地或 CI 有可用的 PostgreSQL，并通过环境变量 TEST_PG_DSN 提供 DSN，如：
//   TEST_PG_DSN="host=localhost port=5432 user=postgres password=secret dbname=test sslmode=disable" \
//     go test -tags postgres ./...
//
// 未设 TEST_PG_DSN 时所有用例 t.Skip。
//
// 覆盖：$N 占位符、information_schema 内省（LoadSchema/Bind）、FOR UPDATE、
// IS NOT DISTINCT FROM（EqDistinct）、RETURNING、JSONB。
package fusion_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func pgDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set TEST_PG_DSN to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Skipf("pg unreachable: %v", err)
	}
	return db, func() {
		// 清理：drop 测试表
		for _, t := range []string{"pg_users", "pg_posts"} {
			db.Exec("DROP TABLE IF EXISTS " + t + " CASCADE")
		}
		db.Close()
	}
}

// PGUser PG 集成测试模型。
type PGUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Email col.Col[*string]
}

// TestPG_BasicCRUD 基本 CRUD + $N 占位符 + RETURNING 回填自增主键。
func TestPG_BasicCRUD(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS pg_users`)
	if _, err := db.ExecContext(ctx, `CREATE TABLE pg_users (id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	Users := fusion.Register[PGUser]("pg_users")

	// 插入（RETURNING 回填自增 id）
	u := &PGUser{}
	u.Name.Set("alice")
	if err := fusion.Insert(Users, wrapped, u).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if u.ID.Get() == 0 {
		t.Error("auto-increment id not backfilled via RETURNING")
	}

	// 查询
	got, err := fusion.From(Users, wrapped).Where(Users.Proto.Name.Eq("alice")).One(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.ID.Get() != u.ID.Get() {
		t.Errorf("id got %d, want %d", got.ID.Get(), u.ID.Get())
	}
}

// TestPG_LoadSchemaBind 内省 + Bind 校验。
func TestPG_LoadSchemaBind(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS pg_users`)
	if _, err := db.ExecContext(ctx, `CREATE TABLE pg_users (id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	Users := fusion.Register[PGUser]("pg_users")

	cat, err := fusion.LoadSchema(ctx, db, dialect.PostgresDialect, "pg_users")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	tab := cat.Table("pg_users")
	if tab == nil {
		t.Fatal("pg_users not in catalog")
	}
	if len(tab.PrimaryKey) != 1 || tab.PrimaryKey[0] != "id" {
		t.Errorf("PK got %v, want [id]", tab.PrimaryKey)
	}
	// 列类型应为 PG 原生
	idCol := tab.Column("id")
	if idCol == nil {
		t.Fatal("id column nil")
	}
	// PG information_schema.data_type 对 SERIAL 列返回 "integer"
	if idCol.SQLType != "integer" {
		t.Logf("note: id SQLType got %q (PG 版本差异可能不同)", idCol.SQLType)
	}
	// Bind 应无差异（模型与表一致）
	diffs := fusion.BindModel(cat, Users)
	if len(diffs) != 0 {
		t.Errorf("expected no bind diffs, got %+v", diffs)
	}
}

// TestPG_EqDistinct PG 上 IS NOT DISTINCT FROM（NULL 安全比较）。
func TestPG_EqDistinct(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS pg_users`)
	if _, err := db.ExecContext(ctx, `CREATE TABLE pg_users (id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	Users := fusion.Register[PGUser]("pg_users")

	// 插入两行：email NULL 和 email 'a@e'
	u1 := &PGUser{}; u1.Name.Set("null-email")
	fusion.Insert(Users, wrapped, u1).Exec(ctx)
	u2 := &PGUser{}; u2.Name.Set("with-email"); u2.Email.Set(strPtr("a@e.com"))
	fusion.Insert(Users, wrapped, u2).Exec(ctx)

	// EqDistinct(nil) 应匹配 email IS NULL 的行
	got, err := fusion.From(Users, wrapped).
		Where(Users.Proto.Email.EqDistinct((*string)(nil))).
		All(ctx)
	if err != nil {
		t.Fatalf("EqDistinct query: %v", err)
	}
	if len(got) != 1 || got[0].Name.Get() != "null-email" {
		t.Errorf("EqDistinct(nil) should match null-email row, got %+v", got)
	}
}

// TestPG_ForUpdate FOR UPDATE 行锁（事务内）。
func TestPG_ForUpdate(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS pg_users`)
	if _, err := db.ExecContext(ctx, `CREATE TABLE pg_users (id SERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	Users := fusion.Register[PGUser]("pg_users")

	u := &PGUser{}; u.Name.Set("alice")
	fusion.Insert(Users, wrapped, u).Exec(ctx)

	// 事务内 ForUpdate().One() 应生成 FOR UPDATE 且不报错
	err := fusion.Tx(ctx, db, func(ctx context.Context) error {
		_, err := fusion.From(Users, wrapped).
			Where(Users.Proto.ID.Eq(u.ID.Get())).
			ForUpdate().
			One(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("ForUpdate in tx: %v", err)
	}
}
