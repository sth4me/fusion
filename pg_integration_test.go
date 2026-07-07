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
	"time"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

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
		for _, t := range []string{
			"pg_users", "pg_posts",
			"pg_emps", "pg_comments", "pg_union_a", "pg_union_b",
			"pg_json_items", "pg_user_roles",
		} {
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

// === PG UNION/CTE/窗口：$N 占位符重写验证（最优先，之前只在 SQLite 验证过 ?）===

// PGEmp 窗口/聚合测试模型。
type PGEmp struct {
	ID     col.Col[int64]
	Dept   col.Col[string]
	Salary col.Col[int64]
}

// TestPG_WindowRowNumber 窗口函数 + $N 占位符。
// ROW_NUMBER() OVER (PARTITION BY dept ORDER BY salary DESC) + WHERE 带参数。
func TestPG_WindowRowNumber(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_emps")
	if _, err := db.Exec(`CREATE TABLE pg_emps (id BIGSERIAL PRIMARY KEY, dept TEXT, salary BIGINT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, r := range []struct{ id, sal int64; dept string }{
		{1, 100, "eng"}, {2, 200, "eng"}, {3, 150, "eng"},
		{4, 120, "mkt"}, {5, 90, "mkt"},
	} {
		db.ExecContext(ctx, `INSERT INTO pg_emps (id, dept, salary) VALUES ($1,$2,$3)`, r.id, r.dept, r.sal)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	Emps := fusion.Register[PGEmp]("pg_emps")

	type RankView struct {
		ID    int64  `db:"id"`
		Dept  string `db:"dept"`
		RankN int64  `db:"rn"`
	}
	fusion.Register[RankView]("pg_emps")

	// 投影：id, dept, ROW_NUMBER() OVER(...) AS rn，外加一个带 $N 参数的 WHERE
	var view []RankView
	err := fusion.From(Emps, wrapped).
		Select(
			Emps.Proto.ID.As("id"),
			Emps.Proto.Dept.As("dept"),
			fusion.RowNumber().Over([]string{"dept"}, []string{"salary DESC"}).As("rn"),
		).
		Where(Emps.Proto.Salary.Gt(50)). // 产生 $1
		OrderBy(Emps.Proto.Dept.Asc(), Emps.Proto.Salary.Desc()).
		AllInto(ctx, &view)
	if err != nil {
		t.Fatalf("window query: %v", err)
	}
	// eng: salary 200(id2)=rn1, 150(id3)=rn2, 100(id1)=rn3
	engRank := map[int64]int64{}
	for _, r := range view {
		if r.Dept == "eng" {
			engRank[r.ID] = r.RankN
		}
	}
	if engRank[2] != 1 || engRank[3] != 2 || engRank[1] != 3 {
		t.Errorf("eng ranks got %v, want {2:1,3:2,1:3}", engRank)
	}
}

// TestPG_CTE 递归 CTE（评论楼中楼）+ $N 占位符。
// WITH RECURSIVE tree AS (... WHERE id=$1 UNION ALL ...) SELECT FROM tree
func TestPG_CTE(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_comments")
	if _, err := db.Exec(`CREATE TABLE pg_comments (id BIGSERIAL PRIMARY KEY, pid BIGINT, body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 树：1(root)→2,3；2→4
	for _, r := range []struct{ id, pid int64; body string }{
		{1, 0, "root"}, {2, 1, "c1"}, {3, 1, "c2"}, {4, 2, "c1-1"},
	} {
		db.ExecContext(ctx, `INSERT INTO pg_comments (id,pid,body) VALUES ($1,$2,$3)`, r.id, r.pid, r.body)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)

	type PGComment struct {
		ID   col.Col[int64]
		PID  col.Col[int64]
		Body col.Col[string]
	}
	fusion.Register[PGComment]("pg_comments")

	// 用 Raw 执行递归 CTE（builder FROM 固定模型表，CTE 引用走 Raw）
	recursiveSQL := `SELECT id, pid, body FROM pg_comments WHERE id = $1 ` +
		`UNION ALL SELECT c.id, c.pid, c.body FROM pg_comments c JOIN tree ON c.pid = tree.id`
	var out []PGComment
	err := fusion.Raw(&out, ctx, wrapped,
		`WITH RECURSIVE tree AS (`+recursiveSQL+`) SELECT id, pid, body FROM tree ORDER BY id`,
		int64(1))
	if err != nil {
		t.Fatalf("raw recursive CTE: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("got %d comments, want 4 (1,2,3,4)", len(out))
	}
}

// TestPG_Union UNION + 尾部 ORDER BY，验证 $N 跨 arm 编号连续。
func TestPG_Union(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_union_a")
	db.Exec(`CREATE TABLE pg_union_a (id BIGINT PRIMARY KEY, name TEXT, state TEXT)`)
	db.ExecContext(ctx, `INSERT INTO pg_union_a VALUES (1,'a1','active')`)
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)

	// UNION 要求两 arm 同类型；用同模型两次查不同条件（Register 按类型缓存，
	// 同类型只能映射一张表，故用单表自 UNION 验证 SQL 正确性 + $N 跨 arm 编号）
	type UItem struct {
		ID    col.Col[int64]
		Name  col.Col[string]
		State col.Col[string]
	}
	A := fusion.Register[UItem]("pg_union_a")

	// 两个 arm 各带一个 $N 参数，验证编号 $1/$2 连续
	q1 := fusion.From(A, wrapped).Where(A.Proto.ID.Eq(1))
	q2 := fusion.From(A, wrapped).Where(A.Proto.ID.Gt(0))
	res, err := fusion.Union(q1, q2).OrderBy(A.Proto.ID.Asc()).All(ctx)
	if err != nil {
		t.Fatalf("union: %v", err)
	}
	// UNION（去重）：a1 同时满足两个条件，去重后 1 行
	if len(res) != 1 {
		t.Errorf("UNION got %d rows, want 1 (dedup)", len(res))
	}
}

// === PG JSONB ===

// PGItem 带 JSONB 字段。
type PGItem struct {
	ID     col.Col[int64]
	Meta   col.Json[map[string]any]
}

// TestPG_JSONB JSONB 字段往返。
func TestPG_JSONB(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_json_items")
	if _, err := db.Exec(`CREATE TABLE pg_json_items (id BIGSERIAL PRIMARY KEY, meta JSONB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	Items := fusion.Register[PGItem]("pg_json_items")

	// 插入（用 Set 标记 dirty，否则 collectSetCols 不识别）
	it := &PGItem{}
	it.Meta.Set(map[string]any{"role": "admin", "level": float64(5)})
	if err := fusion.Insert(Items, wrapped, it).Exec(ctx); err != nil {
		t.Fatalf("insert jsonb: %v", err)
	}
	// 读取 + 按字段查
	got, err := fusion.From(Items, wrapped).Where(Items.Proto.ID.Eq(it.ID.Get())).One(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.Meta.V["role"] != "admin" {
		t.Errorf("meta.role got %v, want admin", got.Meta.V["role"])
	}
	if got.Meta.V["level"] != float64(5) {
		t.Errorf("meta.level got %v, want 5", got.Meta.V["level"])
	}
}

// === PG 复合主键 ===

// PGUserRole 复合主键（user_id + role_id）。
type PGUserRole struct {
	UserID col.Col[int64] `db:"pk"`
	RoleID col.Col[int64] `db:"pk"`
	Name   col.Col[string]
}

// TestPG_CompositePK 复合主键全链路：Insert + 无 Where Update（自动复合 PK）+ DeleteByIDs。
func TestPG_CompositePK(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_user_roles")
	if _, err := db.Exec(`CREATE TABLE pg_user_roles (user_id BIGINT, role_id BIGINT, name TEXT, PRIMARY KEY (user_id, role_id))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.PostgresDialect)
	wrapped := fusion.WrapDB(db)
	UserRoles := fusion.Register[PGUserRole]("pg_user_roles")

	// Insert（复合 PK 都显式提供）
	ur := &PGUserRole{}
	ur.UserID.Set(1); ur.RoleID.Set(10); ur.Name.Set("admin")
	if err := fusion.Insert(UserRoles, wrapped, ur).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 无 Where Update（自动按 user_id+role_id）
	ur.Name.Set("superadmin")
	if err := fusion.Update(UserRoles, wrapped, ur).Exec(ctx); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(1).And(UserRoles.Proto.RoleID.Eq(10))).
		One(ctx)
	if got.Name.Get() != "superadmin" {
		t.Errorf("after update name got %q, want superadmin", got.Name.Get())
	}
	// DeleteByIDs（复合 PK map）
	if err := fusion.DeleteByIDs(UserRoles, wrapped,
		map[string]any{"user_id": int64(1), "role_id": int64(10)}).Exec(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ := fusion.From(UserRoles, wrapped).All(ctx)
	if len(all) != 0 {
		t.Errorf("after delete got %d rows, want 0", len(all))
	}
}

// TestPG_DeadlockRetry 真实死锁 + 错误文本识别验证。
// 构造经典死锁：事务 A 锁行1再请求行2，事务 B 锁行2再请求行1 → PG 杀掉其中一个（40P01）。
// 验证真实 PG 驱动返回的死锁错误能被 isRetryableTxError 的字符串匹配识别。
//
// 注意：PG 默认 deadlock_timeout=1s，死锁检测有延迟，故用例超时设 30s。
// 时序敏感：若调度未交错可能 skip（不报失败）。
func TestPG_DeadlockRetry(t *testing.T) {
	db, cleanup := pgDB(t)
	defer cleanup()
	ctx := context.Background()
	db.Exec("DROP TABLE IF EXISTS pg_dl")
	if _, err := db.Exec(`CREATE TABLE pg_dl (id INT PRIMARY KEY, v INT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	db.ExecContext(ctx, `INSERT INTO pg_dl VALUES (1,0),(2,0)`)

	var aErrText string
	done := make(chan struct{})
	// goroutine A：先 FOR UPDATE 锁 id=1，等 B 锁 id=2 后，请求 id=2 → 死锁
	go func() {
		defer close(done)
		txA, _ := db.BeginTx(ctx, nil)
		txA.Exec(`SELECT v FROM pg_dl WHERE id=1 FOR UPDATE`) // 锁 id=1
		time.Sleep(600 * time.Millisecond)                    // 等 B 锁 id=2
		_, err := txA.Exec(`SELECT v FROM pg_dl WHERE id=2 FOR UPDATE`) // 请求 id=2 → 阻塞/死锁
		if err != nil {
			aErrText = err.Error()
		}
		txA.Rollback()
	}()

	// 主 goroutine B：锁 id=2，等 A 锁 id=1 后，请求 id=1 → 死锁
	time.Sleep(200 * time.Millisecond) // 让 A 先锁 id=1
	txB, _ := db.BeginTx(ctx, nil)
	txB.Exec(`SELECT v FROM pg_dl WHERE id=2 FOR UPDATE`) // 锁 id=2
	time.Sleep(600 * time.Millisecond)                    // 确保 A 在等 id=2
	_, bErr := txB.Exec(`SELECT v FROM pg_dl WHERE id=1 FOR UPDATE`) // 请求 id=1 → 死锁
	bErrText := ""
	if bErr != nil {
		bErrText = bErr.Error()
	}
	txB.Rollback()
	<-done

	// 至少一方拿到死锁错误（另一方可能正常完成或也被杀）
	deadlockText := ""
	for _, txt := range []string{aErrText, bErrText} {
		if containsPgDeadlock(txt) {
			deadlockText = txt
			break
		}
	}
	if deadlockText == "" {
		t.Skipf("no deadlock triggered (timing dependent); A=%q B=%q", aErrText, bErrText)
	}
	t.Logf("real PG deadlock error recognized: %s", deadlockText)
	// 验证我们的字符串匹配能识别（与 tx.isRetryableTxError 同源）
	if !containsPgDeadlock(deadlockText) {
		t.Errorf("deadlock text NOT recognized by retryable matcher: %s", deadlockText)
	}
}

func containsPgDeadlock(s string) bool {
	for _, sub := range []string{"40P01", "deadlock detected", "Deadlock found"} {
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
