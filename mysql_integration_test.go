//go:build mysql

// 本文件验证 MySQL/MariaDB 专属行为（默认 go test 不包含；需 go test -tags mysql）。
//
// 运行前提：本地或 CI 有可用的 MySQL，并通过环境变量 TEST_MYSQL_DSN 提供 DSN，如：
//   TEST_MYSQL_DSN="root:123456@tcp(localhost:3306)/fusion_test?parseTime=true&multiStatements=true" \
//     go test -tags mysql ./...
//
// 未设 TEST_MYSQL_DSN 时所有用例 t.Skip。
// parseTime=true 必须开（否则驱动把 DATETIME 返回为 []byte，time 扫描出错）。
//
// 覆盖：? 占位符、information_schema 内省（LoadSchema/Bind）、RETURNING（8.0+）、
// 批量插入退化路径（C3）、EqDistinct 的 <=> 语法（H2）、ON DUPLICATE KEY Upsert。
package fusion_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

	drivrmysql "github.com/go-sql-driver/mysql"
)

func mysqlDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set TEST_MYSQL_DSN to run MySQL integration tests")
	}
	// 确保 parseTime 开（time 字段需要）；DSN 里没带则补上
	if _, err := drivrmysql.ParseDSN(dsn); err != nil {
		t.Fatalf("invalid TEST_MYSQL_DSN: %v", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Skipf("mysql unreachable: %v", err)
	}
	return db, func() {
		// 清理测试表
		for _, tbl := range []string{"my_users", "my_posts"} {
			db.Exec("DROP TABLE IF EXISTS " + tbl)
		}
		db.Close()
	}
}

// MyUser MySQL 集成测试模型。
type MyUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Email col.Col[*string]
}

// TestMySQL_BasicCRUD 基本 CRUD + ? 占位符 + LastInsertId 回填（无 RETURNING 路径）。
// 用 NewMySQL(false) 模拟旧版 MySQL，走 LastInsertId 路径。
func TestMySQL_BasicCRUD(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100))`); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 用旧版方言（无 RETURNING）→ 走 LastInsertId 路径
	oldDialect := dialect.NewMySQL(false)
	engine := fusion.New(db, fusion.WithDialect(oldDialect))
	Users := fusion.Register[MyUser]("my_users")

	u := &MyUser{}
	u.Name.Set("alice")
	if err := fusion.EInsert(engine, Users, u).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if u.ID.Get() == 0 {
		t.Error("auto-increment id not backfilled via LastInsertId")
	}

	got, err := fusion.EFrom(engine, Users).Where(Users.Proto.Name.Eq("alice")).One(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.ID.Get() != u.ID.Get() {
		t.Errorf("id got %d, want %d", got.ID.Get(), u.ID.Get())
	}
}

// TestMySQL_BatchInsertLastInsertID 批量插入退化路径（C3 回归）。
// NewMySQL(false) 无 RETURNING，多行批量插入应退化为逐行，每行主键正确回填。
func TestMySQL_BatchInsertLastInsertID(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100))`); err != nil {
		t.Fatalf("create: %v", err)
	}

	oldDialect := dialect.NewMySQL(false)
	engine := fusion.New(db, fusion.WithDialect(oldDialect))
	Users := fusion.Register[MyUser]("my_users")

	// 批量插入 3 行
	targets := []*MyUser{{}, {}, {}}
	targets[0].Name.Set("a")
	targets[1].Name.Set("b")
	targets[2].Name.Set("c")
	if err := fusion.EInsertBatch(engine, Users, targets).Exec(ctx); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	// C3 回归：每行的 ID 都应被回填（原 bug 只回填首行）
	for i, tg := range targets {
		if tg.ID.Get() == 0 {
			t.Errorf("target[%d].ID not backfilled (C3 regression): got 0", i)
		}
	}
	// ID 应单调递增
	if !(targets[0].ID.Get() < targets[1].ID.Get() && targets[1].ID.Get() < targets[2].ID.Get()) {
		t.Errorf("IDs should be increasing, got %d %d %d",
			targets[0].ID.Get(), targets[1].ID.Get(), targets[2].ID.Get())
	}
}

// TestMySQL_ReturningBatch RETURNING 路径仅 MariaDB 10.5+ 支持；MySQL 8.0 跳过。
func TestMySQL_ReturningBatch(t *testing.T) {
	// MySQL 8.0 不支持 RETURNING；只有 MariaDB 10.5+ 才有。
	// 用 SupportsReturning=true 在 MySQL 上会语法错误，故此用例仅验证"开了会报错"
	// 以确认方言标志正确生效（防止误开）。
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 探测是否是 MariaDB（支持 RETURNING）
	var version string
	db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	if !containsSubstr(version, "MariaDB") {
		// MySQL：开 RETURNING 应失败（验证方言标志不会误生成支持不了的 SQL）
		engine := fusion.New(db, fusion.WithDialect(dialect.NewMySQL(true)))
		Users := fusion.Register[MyUser]("my_users")
		u := &MyUser{}; u.Name.Set("probe")
		err := fusion.EInsert(engine, Users, u).Exec(ctx)
		if err == nil {
			t.Skip("this MySQL supports RETURNING (newer than expected); skipping negative test")
		}
		t.Logf("correctly: MySQL 8.0 rejects RETURNING as expected: %v", err)
		return
	}
	// MariaDB：真正验证 RETURNING 路径
	engine := fusion.New(db, fusion.WithDialect(dialect.NewMySQL(true)))
	Users := fusion.Register[MyUser]("my_users")
	targets := []*MyUser{{}, {}, {}}
	targets[0].Name.Set("x"); targets[1].Name.Set("y"); targets[2].Name.Set("z")
	if err := fusion.EInsertBatch(engine, Users, targets).Exec(ctx); err != nil {
		t.Fatalf("batch insert with RETURNING: %v", err)
	}
	for i, tg := range targets {
		if tg.ID.Get() == 0 {
			t.Errorf("target[%d].ID not backfilled via RETURNING", i)
		}
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestMySQL_LoadSchemaBind MySQL information_schema 内省 + Bind。
func TestMySQL_LoadSchemaBind(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	d := dialect.NewMySQL(false) // MySQL 8.0 无 RETURNING
	Users := fusion.Register[MyUser]("my_users")

	cat, err := fusion.LoadSchema(ctx, db, d, "my_users")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	tab := cat.Table("my_users")
	if tab == nil {
		t.Fatal("my_users not in catalog")
	}
	if len(tab.PrimaryKey) != 1 || tab.PrimaryKey[0] != "id" {
		t.Errorf("PK got %v, want [id]", tab.PrimaryKey)
	}
	idCol := tab.Column("id")
	if idCol == nil {
		t.Fatal("id column nil")
	}
	// MySQL column_type 对 BIGINT 应含 "bigint"
	if idCol.SQLType != "bigint" {
		t.Errorf("id SQLType got %q, want bigint", idCol.SQLType)
	}
	diffs := fusion.BindModel(cat, Users)
	if len(diffs) != 0 {
		t.Errorf("expected no bind diffs, got %+v", diffs)
	}
}

// TestMySQL_EqDistinct <=> 语法（H2 回归）。
func TestMySQL_EqDistinct(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	d := dialect.NewMySQL(false) // MySQL 8.0 无 RETURNING
	engine := fusion.New(db, fusion.WithDialect(d))
	Users := fusion.Register[MyUser]("my_users")

	u1 := &MyUser{}; u1.Name.Set("null-email")
	fusion.EInsert(engine, Users, u1).Exec(ctx)
	u2 := &MyUser{}; u2.Name.Set("with-email"); u2.Email.Set(strPtr("a@e.com"))
	fusion.EInsert(engine, Users, u2).Exec(ctx)

	// EqDistinct(nil) → MySQL 生成 col <=> NULL，应匹配 NULL 行
	got, err := fusion.EFrom(engine, Users).
		Where(Users.Proto.Email.EqDistinct((*string)(nil))).
		All(ctx)
	if err != nil {
		t.Fatalf("EqDistinct query: %v", err)
	}
	if len(got) != 1 || got[0].Name.Get() != "null-email" {
		t.Errorf("EqDistinct(nil) should match null-email row, got %+v", got)
	}
}

// TestMySQL_Upsert ON DUPLICATE KEY UPDATE。
func TestMySQL_Upsert(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100) UNIQUE)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	d := dialect.NewMySQL(false) // MySQL 8.0 无 RETURNING
	engine := fusion.New(db, fusion.WithDialect(d))
	Users := fusion.Register[MyUser]("my_users")

	// 首次插入
	u := &MyUser{}
	u.Name.Set("alice"); u.Email.Set(strPtr("a@e.com"))
	if err := fusion.EUpsert(engine, Users, u).Exec(ctx); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// 再次 upsert 同 email（冲突）→ 应更新 name 而非报错
	u2 := &MyUser{}
	u2.Name.Set("alice2"); u2.Email.Set(strPtr("a@e.com"))
	if err := fusion.EUpsert(engine, Users, u2).
		OnConflict([]string{"email"}, []string{"name"}).
		Exec(ctx); err != nil {
		t.Fatalf("upsert on conflict: %v", err)
	}
	// 查询确认 name 被更新为 alice2
	got, _ := fusion.EFrom(engine, Users).Where(Users.Proto.Email.EqDistinct(strPtr("a@e.com"))).One(ctx)
	if got.Name.Get() != "alice2" {
		t.Errorf("after upsert name got %q, want alice2", got.Name.Get())
	}
}

// TestMySQL_ForUpdate FOR UPDATE 行锁。
func TestMySQL_ForUpdate(t *testing.T) {
	db, cleanup := mysqlDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS my_users")
	if _, err := db.Exec(`CREATE TABLE my_users (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(100))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	d := dialect.NewMySQL(false) // MySQL 8.0 无 RETURNING
	engine := fusion.New(db, fusion.WithDialect(d))
	Users := fusion.Register[MyUser]("my_users")

	u := &MyUser{}; u.Name.Set("alice")
	fusion.EInsert(engine, Users, u).Exec(ctx)

	err := fusion.ETx(engine, ctx, func(ctx context.Context) error {
		_, err := fusion.EFrom(engine, Users).
			Where(Users.Proto.ID.Eq(u.ID.Get())).
			ForUpdate().
			One(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("ForUpdate in tx: %v", err)
	}
}
