package col

import (
	"encoding/json"
	"testing"
	"time"
)

// 辅助 renderer（复用 col_test.go 的 miniR，但这里独立避免耦合）
type r2 struct{ n int }

func (m *r2) NextPlaceholder() string { m.n++; return "?" }
func (m *r2) AddParam(any)            {}
func (m *r2) QuoteCol(tc string) string {
	out := ""
	for i, p := range splitDots(tc) {
		if i > 0 {
			out += "."
		}
		out += "`" + p + "`"
	}
	return out
}
func splitDots(s string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	out = append(out, cur)
	return out
}

func TestInExpr(t *testing.T) {
	var c Col[int]
	c.col = "id"
	c.table = "t0"
	e := c.In([]int{1, 2, 3})
	r := &r2{}
	s := e.Render(r)
	want := "`t0`.`id` IN (?, ?, ?)"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
	if r.n != 3 {
		t.Errorf("placeholder count got %d, want 3", r.n)
	}
}

func TestIsNullExpr(t *testing.T) {
	var c Col[int]
	c.col = "deleted_at"
	c.table = "t0"
	r := &r2{}
	if s := c.IsNull().Render(r); s != "`t0`.`deleted_at` IS NULL" {
		t.Errorf("IsNull got %q", s)
	}
	if s := c.IsNotNull().Render(r); s != "`t0`.`deleted_at` IS NOT NULL" {
		t.Errorf("IsNotNull got %q", s)
	}
}

func TestEqCol(t *testing.T) {
	var a, b Col[int64]
	a.col, a.table = "dept_id", "t0"
	b.col, b.table = "id", "t1"
	r := &r2{}
	s := a.EqCol(b).Render(r)
	want := "`t0`.`dept_id` = `t1`.`id`"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

func TestOrderRender(t *testing.T) {
	var c Col[int]
	c.col, c.table = "age", "t0"
	r := &r2{}
	if s := c.Asc().RenderClause(r); s != "`t0`.`age` ASC" {
		t.Errorf("Asc got %q", s)
	}
	if s := c.Desc().RenderClause(r); s != "`t0`.`age` DESC" {
		t.Errorf("Desc got %q", s)
	}
}

func TestAllComparisons(t *testing.T) {
	var c Col[int]
	c.col, c.table = "n", "t"
	r := &r2{}
	cases := []struct {
		op   string
		expr func() string
	}{
		{"=", func() string { return c.Eq(1).Render(r) }},
		{"<>", func() string { return c.Ne(1).Render(r) }},
		{">", func() string { return c.Gt(1).Render(r) }},
		{">=", func() string { return c.Gte(1).Render(r) }},
		{"<", func() string { return c.Lt(1).Render(r) }},
		{"<=", func() string { return c.Lte(1).Render(r) }},
	}
	for _, tc := range cases {
		want := "`t`.`n` " + tc.op + " ?"
		if got := tc.expr(); got != want {
			t.Errorf("op %s got %q, want %q", tc.op, got, want)
		}
	}
}

func TestScanTimeString(t *testing.T) {
	var c Col[time.Time]
	// 扫描 RFC3339 字符串
	if err := c.Scan("2026-01-02T15:04:05Z"); err != nil {
		t.Fatalf("scan time: %v", err)
	}
	got := c.Get()
	want := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanTimeValue(t *testing.T) {
	var c Col[time.Time]
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := c.Scan(now); err != nil {
		t.Fatalf("scan time value: %v", err)
	}
	if !c.Get().Equal(now) {
		t.Errorf("got %v", c.Get())
	}
}

// TestScanTimeParseError 验证无法解析的时间字符串返回 error，
// 而非静默置零（回归：之前会默默设成 0001-01-01 造成数据损坏）。
func TestScanTimeParseError(t *testing.T) {
	var c Col[time.Time]
	err := c.Scan("not-a-timestamp")
	if err == nil {
		t.Fatalf("expected error for unparseable time, got nil (value=%v)", c.Get())
	}
	if got := c.Get(); !got.IsZero() {
		t.Errorf("on parse failure, value should remain zero, got %v", got)
	}
	// []byte 路径同样应报错
	var c2 Col[time.Time]
	if err := c2.Scan([]byte("also-bad")); err == nil {
		t.Fatalf("expected error for unparseable []byte time, got nil")
	}
}

func TestScanIntTypes(t *testing.T) {
	// 不同整型源值扫描到 int64
	var c Col[int64]
	if err := c.Scan(int(42)); err != nil {
		t.Fatalf("scan int: %v", err)
	}
	if c.Get() != 42 {
		t.Errorf("got %d", c.Get())
	}
}

func TestValueIntTypes(t *testing.T) {
	// int/int32 等 Value 时应转 int64（driver.Value 规范）
	var c Col[int32]
	c.Set(100)
	v, err := c.Value()
	if err != nil {
		t.Fatal(err)
	}
	if i, ok := v.(int64); !ok || i != 100 {
		t.Errorf("got %v, want int64(100)", v)
	}
}

func TestIsZero(t *testing.T) {
	var c Col[int]
	if !c.IsZero() {
		t.Error("zero Col should be IsZero")
	}
	c.Set(0)
	if c.IsZero() {
		t.Error("Set(0) should not be IsZero (set=true)")
	}
}

// TestTableName 验证 TableName 返回注册时填的表名（替代已删的 SetTableAlias）
func TestTableName(t *testing.T) {
	var c Col[int]
	c.col = "id"
	c.table = "users"
	if c.TableName() != "users" {
		t.Errorf("got %q, want users", c.TableName())
	}
}

// TestPtrColJSON 验证 Col[*T] 的 JSON 序列化（有值 vs nil）
func TestPtrColJSON(t *testing.T) {
	type W struct {
		Email Col[*string]
	}
	var w W
	w.Email.Set(strPtr("a@b.com"))
	b, _ := jsonM(w)
	if !containsStr(string(b), `"Email":"a@b.com"`) {
		t.Errorf("ptr json got %s", b)
	}

	var w2 W // Email 为 nil
	b2, _ := jsonM(w2)
	if !containsStr(string(b2), `"Email":null`) {
		t.Errorf("nil ptr json got %s", b2)
	}
}

func strPtr(s string) *string { return &s }

func jsonM(v any) ([]byte, error) {
	return json.Marshal(v)
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
