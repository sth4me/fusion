package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// UUIDItem 验证 col.Col[uuid.UUID] 的往返（主键 + 外键 + 可空）。
type UUIDItem struct {
	ID       col.Col[uuid.UUID]  // 主键 UUID
	ParentID col.Col[uuid.UUID]  // 非空 UUID
	RefID    col.Col[*uuid.UUID] // 可空 UUID（nil = NULL）
}

// TestE2E_UUIDRoundTrip_SQLite SQLite 上 UUID 往返（存为 TEXT）。
func TestE2E_UUIDRoundTrip_SQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE uuid_items (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL, ref_id TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	wrapped := fusion.WrapDB(db)
	Items := fusion.Register[UUIDItem]("uuid_items")
	ctx := context.Background()

	// 生成 UUIDv7（时间有序）
	id := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	refID := uuid.Must(uuid.NewV7())

	// 行1：RefID 有值
	it1 := &UUIDItem{}
	it1.ID.Set(id)
	it1.ParentID.Set(parentID)
	it1.RefID.Set(&refID)
	if err := fusion.Insert(Items, wrapped, it1).Exec(ctx); err != nil {
		t.Fatalf("insert row1: %v", err)
	}

	// 行2：RefID 为 NULL
	it2 := &UUIDItem{}
	it2.ID.Set(uuid.Must(uuid.NewV7()))
	it2.ParentID.Set(parentID)
	if err := fusion.Insert(Items, wrapped, it2).Exec(ctx); err != nil {
		t.Fatalf("insert row2: %v", err)
	}

	got, err := fusion.From(Items, wrapped).OrderBy(Items.Proto.ID.Asc()).All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	// 行1：ID 往返一致
	if got[0].ID.Get() != id {
		t.Errorf("row1 ID got %v, want %v", got[0].ID.Get(), id)
	}
	if got[0].ParentID.Get() != parentID {
		t.Errorf("row1 ParentID got %v, want %v", got[0].ParentID.Get(), parentID)
	}
	if got[0].RefID.Get() == nil || *got[0].RefID.Get() != refID {
		t.Errorf("row1 RefID got %v, want %v", got[0].RefID.Get(), refID)
	}

	// 行2：RefID 为 nil
	if got[1].RefID.Get() != nil {
		t.Errorf("row2 RefID got %v, want nil", got[1].RefID.Get())
	}

	// 按 UUID 查询（WHERE id = ?）
	one, err := fusion.From(Items, wrapped).Where(Items.Proto.ID.Eq(id)).One(ctx)
	if err != nil {
		t.Fatalf("query by uuid: %v", err)
	}
	if one.ID.Get() != id {
		t.Errorf("query by uuid got %v, want %v", one.ID.Get(), id)
	}
}

// TestE2E_UUIDGeneratedInApp 验证应用层预生成 UUID 后 Insert（不依赖 DB RETURNING）。
func TestE2E_UUIDGeneratedInApp(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE uuid_items (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL, ref_id TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	wrapped := fusion.WrapDB(db)
	Items := fusion.Register[UUIDItem]("uuid_items")

	// 应用层预生成 UUID（UUIDv7 策略的核心场景）
	genID := uuid.Must(uuid.NewV7())
	it := &UUIDItem{}
	it.ID.Set(genID)
	it.ParentID.Set(uuid.Must(uuid.NewV7()))
	if err := fusion.Insert(Items, wrapped, it).Exec(context.Background()); err != nil {
		t.Fatalf("insert with pre-generated uuid: %v", err)
	}
	// ID 应和预生成的一致（不靠 DB 回填）
	if it.ID.Get() != genID {
		t.Errorf("pre-generated ID changed after insert: got %v, want %v", it.ID.Get(), genID)
	}
}
