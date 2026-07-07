package fusion_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// NullProbe 覆盖各类可空字段类型（核心机制：set 标志 + 指针 nil = NULL）。
type NullProbe struct {
	ID      col.Col[int64]
	I       col.Col[*int64]     // 可空 int
	F       col.Col[*float64]   // 可空 float
	B       col.Col[*bool]      // 可空 bool
	S       col.Col[*string]    // 可空 string（已测，对照组）
	T       col.Col[*time.Time] // 可空 time
	PlainI  col.Col[int64]      // 非指针 int（NOT NULL 列；不测 NULL）
}

func setupNullTypesDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE null_probes (
		id INTEGER PRIMARY KEY,
		i INTEGER,
		f REAL,
		b INTEGER,
		s TEXT,
		t TEXT,
		plain_i INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return fusion.WrapDB(db), db
}

// TestE2E_NullTypesRoundTrip 各类可空字段的 NULL 插入 + 读取往返。
// 验证 set 标志机制对 bool/int/float/time 都正确（之前只测了 *string）。
func TestE2E_NullTypesRoundTrip(t *testing.T) {
	wrapped, raw := setupNullTypesDB(t)
	defer raw.Close()
	Probes := fusion.Register[NullProbe]("null_probes")
	ctx := context.Background()

	// 行 1：所有可空字段为 NULL（指针 nil）
	row1 := &NullProbe{}
	row1.PlainI.Set(100) // NOT NULL 列必须有值
	// 可空字段不 Set → 插入 NULL
	if err := fusion.Insert(Probes, wrapped, row1).Exec(ctx); err != nil {
		t.Fatalf("insert null row: %v", err)
	}

	// 行 2：所有可空字段有值
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	row2 := &NullProbe{}
	row2.PlainI.Set(200)
	iVal := int64(42)
	fVal := 3.14
	bVal := true
	row2.I.Set(&iVal)
	row2.F.Set(&fVal)
	row2.B.Set(&bVal)
	row2.S.Set(strPtr("hello"))
	row2.T.Set(&now)
	if err := fusion.Insert(Probes, wrapped, row2).Exec(ctx); err != nil {
		t.Fatalf("insert valued row: %v", err)
	}

	got, err := fusion.From(Probes, wrapped).OrderBy(Probes.Proto.ID.Asc()).All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	// 行 1：所有可空字段应为 nil（NULL）
	r1 := got[0]
	if r1.I.Get() != nil {
		t.Errorf("row1 I (NULL) got %v, want nil", r1.I.Get())
	}
	if r1.F.Get() != nil {
		t.Errorf("row1 F (NULL) got %v, want nil", r1.F.Get())
	}
	if r1.B.Get() != nil {
		t.Errorf("row1 B (NULL) got %v, want nil", r1.B.Get())
	}
	if r1.T.Get() != nil {
		t.Errorf("row1 T (NULL) got %v, want nil", r1.T.Get())
	}
	if r1.S.Get() != nil {
		t.Errorf("row1 S (NULL) got %v, want nil", r1.S.Get())
	}

	// 行 2：值应正确往返
	r2 := got[1]
	if r2.I.Get() == nil || *r2.I.Get() != 42 {
		t.Errorf("row2 I got %v, want 42", r2.I.Get())
	}
	if r2.F.Get() == nil || *r2.F.Get() != 3.14 {
		t.Errorf("row2 F got %v, want 3.14", r2.F.Get())
	}
	if r2.B.Get() == nil || *r2.B.Get() != true {
		t.Errorf("row2 B got %v, want true", r2.B.Get())
	}
	if r2.S.Get() == nil || *r2.S.Get() != "hello" {
		t.Errorf("row2 S got %v, want hello", r2.S.Get())
	}
	if r2.T.Get() == nil {
		t.Error("row2 T got nil, want time")
	} else if !r2.T.Get().Equal(now) {
		t.Errorf("row2 T got %v, want %v", r2.T.Get(), now)
	}
}

// TestE2E_NullTypes_QueryByNull 用 EqDistinct 查 NULL（NULL 安全比较）。
func TestE2E_NullTypes_QueryByNull(t *testing.T) {
	wrapped, raw := setupNullTypesDB(t)
	defer raw.Close()
	Probes := fusion.Register[NullProbe]("null_probes")
	ctx := context.Background()

	// 插入一行 I 为 NULL
	row := &NullProbe{}
	row.PlainI.Set(1)
	fusion.Insert(Probes, wrapped, row).Exec(ctx)
	// 插入一行 I 有值
	row2 := &NullProbe{}
	row2.PlainI.Set(2)
	iVal := int64(99)
	row2.I.Set(&iVal)
	fusion.Insert(Probes, wrapped, row2).Exec(ctx)

	// EqDistinct(nil) 查 I 为 NULL 的行（SQLite 支持 IS NOT DISTINCT FROM）
	got, err := fusion.From(Probes, wrapped).
		Where(Probes.Proto.I.EqDistinct((*int64)(nil))).
		All(ctx)
	if err != nil {
		t.Fatalf("query null by EqDistinct: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d rows with I IS NULL, want 1", len(got))
	}
}
