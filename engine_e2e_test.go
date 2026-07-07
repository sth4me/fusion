package fusion_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// Engine 测试模型（独立，避免缓存冲突）。
type EUser struct {
	ID   col.Col[int64]
	Name col.Col[string]
	Age  col.Col[int]
}

func openEngineDB(t *testing.T, ddl string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

// TestE2E_EngineBasic 基本：New + EFrom/EInsert 全流程。
func TestE2E_EngineBasic(t *testing.T) {
	db := openEngineDB(t, `CREATE TABLE eusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	defer db.Close()
	Users := fusion.Register[EUser]("eusers")

	engine := fusion.New(db, fusion.WithDialect(dialect.SQLiteDialect))

	// 插入
	u := &EUser{}
	u.Name.Set("alice")
	u.Age.Set(30)
	if err := fusion.EInsert(engine, Users, u).Exec(context.Background()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if u.ID.Get() != 1 {
		t.Errorf("auto-increment ID got %d, want 1", u.ID.Get())
	}

	// 查询
	got, err := fusion.EFrom(engine, Users).
		Where(Users.Proto.Name.Eq("alice")).
		One(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.Age.Get() != 30 {
		t.Errorf("age got %d", got.Age.Get())
	}

	// 更新（无 Where → 自动按主键）
	got.Age.Set(31)
	if err := fusion.EUpdate(engine, Users, &got).Exec(context.Background()); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := fusion.EFrom(engine, Users).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	if again.Age.Get() != 31 {
		t.Errorf("after update age got %d, want 31", again.Age.Get())
	}
}

// TestE2E_EngineMultiIsolation 多 Engine 不同方言互不干扰（核心价值）。
// 用两个独立 sqlite 库模拟"多库"：每个 Engine 各自的 db + 方言，互不串。
func TestE2E_EngineMultiIsolation(t *testing.T) {
	db1 := openEngineDB(t, `CREATE TABLE eusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	defer db1.Close()
	db2 := openEngineDB(t, `CREATE TABLE eusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	defer db2.Close()
	Users := fusion.Register[EUser]("eusers")

	e1 := fusion.New(db1, fusion.WithDialect(dialect.SQLiteDialect))
	e2 := fusion.New(db2, fusion.WithDialect(dialect.SQLiteDialect))

	// 各自插入
	u1 := &EUser{}; u1.Name.Set("from-e1"); u1.Age.Set(1)
	fusion.EInsert(e1, Users, u1).Exec(context.Background())
	u2 := &EUser{}; u2.Name.Set("from-e2"); u2.Age.Set(2)
	fusion.EInsert(e2, Users, u2).Exec(context.Background())

	// e1 只能看到 from-e1，e2 只能看到 from-e2
	got1, _ := fusion.EFrom(e1, Users).OrderBy(Users.Proto.ID.Asc()).All(context.Background())
	if len(got1) != 1 || got1[0].Name.Get() != "from-e1" {
		t.Errorf("e1 should see only from-e1, got %+v", got1)
	}
	got2, _ := fusion.EFrom(e2, Users).OrderBy(Users.Proto.ID.Asc()).All(context.Background())
	if len(got2) != 1 || got2[0].Name.Get() != "from-e2" {
		t.Errorf("e2 should see only from-e2, got %+v", got2)
	}
}

// TestE2E_EnginePerEngineLogger 各 Engine 可记不同 logger。
func TestE2E_EnginePerEngineLogger(t *testing.T) {
	db := openEngineDB(t, `CREATE TABLE eusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	defer db.Close()
	Users := fusion.Register[EUser]("eusers")

	var buf1, buf2 bytes.Buffer
	l1 := slog.New(slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l2 := slog.New(slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelDebug}))

	e1 := fusion.New(db, fusion.WithDialect(dialect.SQLiteDialect), fusion.WithLogger(l1))
	e2 := fusion.New(db, fusion.WithDialect(dialect.SQLiteDialect), fusion.WithLogger(l2))

	u := &EUser{}; u.Name.Set("a")
	fusion.EInsert(e1, Users, u).Exec(e1.Ctx(context.Background()))
	fusion.EFrom(e2, Users).All(e2.Ctx(context.Background()))

	// e1 的写操作应进 buf1（含 INSERT），e2 的读操作应进 buf2（含 SELECT）
	if !strings.Contains(buf1.String(), "INSERT") {
		t.Errorf("e1 logger should capture INSERT, got:\n%s", buf1.String())
	}
	if !strings.Contains(buf2.String(), "SELECT") {
		t.Errorf("e2 logger should capture SELECT, got:\n%s", buf2.String())
	}
	// 互不串：e1 logger 不应有 e2 的 SELECT（这里 e1 只做了 INSERT）
	if strings.Contains(buf1.String(), "SELECT") {
		t.Errorf("e1 logger should NOT have e2's SELECT (isolation), got:\n%s", buf1.String())
	}
}

// TestE2E_EngineTx Engine.Tx 用默认事务选项；fn 返回 error 自动回滚。
func TestE2E_EngineTx(t *testing.T) {
	db := openEngineDB(t, `CREATE TABLE eusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	defer db.Close()
	Users := fusion.Register[EUser]("eusers")
	engine := fusion.New(db, fusion.WithDialect(dialect.SQLiteDialect))

	// 提交路径
	err := fusion.ETx(engine, context.Background(), func(ctx context.Context) error {
		u := &EUser{}; u.Name.Set("tx-commit"); u.Age.Set(1)
		return fusion.EInsert(engine, Users, u).Exec(ctx)
	})
	if err != nil {
		t.Fatalf("ETx commit: %v", err)
	}
	all, _ := fusion.EFrom(engine, Users).All(context.Background())
	if len(all) != 1 || all[0].Name.Get() != "tx-commit" {
		t.Errorf("after commit got %+v", all)
	}

	// 回滚路径
	err = fusion.ETx(engine, context.Background(), func(ctx context.Context) error {
		u := &EUser{}; u.Name.Set("tx-rollback"); u.Age.Set(2)
		fusion.EInsert(engine, Users, u).Exec(ctx)
		return sql.ErrNoRows // 任意 error → 回滚
	})
	if err == nil {
		t.Fatal("should return error (rollback)")
	}
	// 回滚后仍只有 1 条
	all2, _ := fusion.EFrom(engine, Users).All(context.Background())
	if len(all2) != 1 {
		t.Errorf("after rollback got %d rows, want 1", len(all2))
	}
}
