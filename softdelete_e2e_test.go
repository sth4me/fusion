package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

	_ "modernc.org/sqlite"
)

// SDItem 软删除测试模型（带 col.SoftDelete 字段）。
type SDItem struct {
	ID       col.Col[int64]
	Name     col.Col[string]
	Deleted  col.SoftDelete // 软删除字段 → deleted_at 列
}

func setupSoftDeleteDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sd_items (id INTEGER PRIMARY KEY, name TEXT, deleted_at TIMESTAMP)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return fusion.WrapDB(db), db
}

// TestE2E_SoftDelete_QueryAutoFilter 查询自动过滤已软删除的行。
func TestE2E_SoftDelete_QueryAutoFilter(t *testing.T) {
	wrapped, raw := setupSoftDeleteDB(t)
	defer raw.Close()
	Items := fusion.Register[SDItem]("sd_items")
	ctx := context.Background()

	// 插入 3 行
	for _, name := range []string{"a", "b", "c"} {
		it := &SDItem{}
		it.Name.Set(name)
		fusion.Insert(Items, wrapped, it).Exec(ctx)
	}

	// 软删除 id=2（b）
	fusion.DeleteByID(Items, wrapped, int64(2)).Exec(ctx)

	// 普通查询：应只看到 a 和 c（b 被软删除，自动过滤）
	all, _ := fusion.From(Items, wrapped).OrderBy(Items.Proto.ID.Asc()).All(ctx)
	if len(all) != 2 {
		t.Fatalf("got %d rows, want 2 (soft-deleted filtered)", len(all))
	}
	if all[0].Name.Get() != "a" || all[1].Name.Get() != "c" {
		t.Errorf("got names %s, %s, want a, c", all[0].Name.Get(), all[1].Name.Get())
	}

	// Unscoped 查询：应看到全部 3 行（含已删除的 b）
	allUnscoped, _ := fusion.From(Items, wrapped).Unscoped().OrderBy(Items.Proto.ID.Asc()).All(ctx)
	if len(allUnscoped) != 3 {
		t.Fatalf("Unscoped got %d rows, want 3 (include soft-deleted)", len(allUnscoped))
	}
	// b 的 Deleted 字段应非 nil（已标记删除时间）
	if !allUnscoped[1].Deleted.IsDeleted() {
		t.Error("row b should have deleted_at set (IsDeleted=true)")
	}
	// a 和 c 的 Deleted 应为 nil（未删除）
	if allUnscoped[0].Deleted.IsDeleted() || allUnscoped[2].Deleted.IsDeleted() {
		t.Error("rows a and c should not be deleted")
	}
}

// TestE2E_SoftDelete_OneAutoFilter One 查询也自动过滤软删除。
func TestE2E_SoftDelete_OneAutoFilter(t *testing.T) {
	wrapped, raw := setupSoftDeleteDB(t)
	defer raw.Close()
	Items := fusion.Register[SDItem]("sd_items")
	ctx := context.Background()

	it := &SDItem{}
	it.Name.Set("x")
	fusion.Insert(Items, wrapped, it).Exec(ctx)
	id := it.ID.Get()

	// 软删除
	fusion.DeleteByID(Items, wrapped, id).Exec(ctx)

	// 普通 One：应查不到（已软删除）→ ErrNotFound
	_, err := fusion.From(Items, wrapped).Where(Items.Proto.ID.Eq(id)).One(ctx)
	if err == nil {
		t.Fatal("One on soft-deleted row should return not-found error")
	}

	// Unscoped One：应能查到
	got, err := fusion.From(Items, wrapped).Unscoped().Where(Items.Proto.ID.Eq(id)).One(ctx)
	if err != nil {
		t.Fatalf("Unscoped One should find soft-deleted row: %v", err)
	}
	if got.Name.Get() != "x" {
		t.Errorf("got name %s, want x", got.Name.Get())
	}
}

// TestE2E_SoftDelete_CountAutoFilter Count 也自动过滤。
func TestE2E_SoftDelete_CountAutoFilter(t *testing.T) {
	wrapped, raw := setupSoftDeleteDB(t)
	defer raw.Close()
	Items := fusion.Register[SDItem]("sd_items")
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c", "d"} {
		it := &SDItem{}
		it.Name.Set(name)
		fusion.Insert(Items, wrapped, it).Exec(ctx)
	}
	// 删 2 个
	fusion.DeleteByID(Items, wrapped, int64(1)).Exec(ctx)
	fusion.DeleteByID(Items, wrapped, int64(2)).Exec(ctx)

	// Count 自动过滤 → 2
	cnt, _ := fusion.From(Items, wrapped).Count(ctx)
	if cnt != 2 {
		t.Errorf("Count got %d, want 2 (auto-filtered soft-deleted)", cnt)
	}

	// Unscoped Count → 4
	cntAll, _ := fusion.From(Items, wrapped).Unscoped().Count(ctx)
	if cntAll != 4 {
		t.Errorf("Unscoped Count got %d, want 4", cntAll)
	}
}

// TestE2E_SoftDelete_IDempotent 重复软删除同一行不报错（WHERE deleted_at IS NULL 使第二次无影响行）。
func TestE2E_SoftDelete_IDempotent(t *testing.T) {
	wrapped, raw := setupSoftDeleteDB(t)
	defer raw.Close()
	Items := fusion.Register[SDItem]("sd_items")
	ctx := context.Background()

	it := &SDItem{}
	it.Name.Set("once")
	fusion.Insert(Items, wrapped, it).Exec(ctx)
	id := it.ID.Get()

	// 第一次软删除
	if err := fusion.DeleteByID(Items, wrapped, id).Exec(ctx); err != nil {
		t.Fatalf("first soft delete: %v", err)
	}
	// 第二次（行已 deleted_at 非空，WHERE deleted_at IS NULL 匹配不到 → 无影响行，不报错）
	if err := fusion.DeleteByID(Items, wrapped, id).Exec(ctx); err != nil {
		t.Fatalf("second soft delete (idempotent): %v", err)
	}
}

// TestE2E_SoftDelete_InsertNoDeletedAt 插入时不带 deleted_at（NULL = 未删除）。
func TestE2E_SoftDelete_InsertNoDeletedAt(t *testing.T) {
	wrapped, raw := setupSoftDeleteDB(t)
	defer raw.Close()
	Items := fusion.Register[SDItem]("sd_items")
	ctx := context.Background()

	it := &SDItem{}
	it.Name.Set("fresh")
	if err := fusion.Insert(Items, wrapped, it).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := fusion.From(Items, wrapped).Where(Items.Proto.Name.Eq("fresh")).One(ctx)
	if got.Deleted.IsDeleted() {
		t.Error("freshly inserted row should not be deleted (deleted_at NULL)")
	}
}
