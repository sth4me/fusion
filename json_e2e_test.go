package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// JsonItem SQLite 上的 Json 字段往返测试模型。
// 之前 col.Json[T] 没实现 SetMeta，列名恒为空，DML 生成空列名 → 从未真实跑通。
type JsonItem struct {
	ID   col.Col[int64]
	Meta col.Json[map[string]any]
	Tags col.Json[[]string]
}

func setupJsonDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE json_items (id INTEGER PRIMARY KEY, meta TEXT, tags TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return db
}

// TestE2E_JsonRoundTrip SQLite 上 Json 字段插入 + 读取往返。
// 覆盖 map 和 slice 两种泛型实例；验证 SetMeta 修复后列名正确填充。
func TestE2E_JsonRoundTrip(t *testing.T) {
	db := setupJsonDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	Items := fusion.Register[JsonItem]("json_items")
	ctx := context.Background()

	// 插入（必须用 Set 标记 dirty）
	it := &JsonItem{}
	it.Meta.Set(map[string]any{"role": "admin", "level": float64(5)})
	it.Tags.Set([]string{"go", "orm"})
	if err := fusion.Insert(Items, wrapped, it).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if it.ID.Get() == 0 {
		t.Error("id not backfilled")
	}

	// 读取
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
	if len(got.Tags.V) != 2 || got.Tags.V[0] != "go" || got.Tags.V[1] != "orm" {
		t.Errorf("tags got %v, want [go orm]", got.Tags.V)
	}
}

// TestE2E_JsonNull Json 字段未 Set → 插入应跳过（不进 INSERT 列）。
func TestE2E_JsonNull(t *testing.T) {
	db := setupJsonDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	Items := fusion.Register[JsonItem]("json_items")
	ctx := context.Background()

	// 只 Set ID，Meta/Tags 不 Set
	it := &JsonItem{}
	it.ID.Set(42)
	if err := fusion.Insert(Items, wrapped, it).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 读取：Meta/Tags 应为零值（DB NULL）
	got, err := fusion.From(Items, wrapped).Where(Items.Proto.ID.Eq(42)).One(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.Meta.V["role"] != nil {
		t.Errorf("unset Meta should be nil/zero, got %v", got.Meta.V)
	}
	if len(got.Tags.V) != 0 {
		t.Errorf("unset Tags should be empty, got %v", got.Tags.V)
	}
}
