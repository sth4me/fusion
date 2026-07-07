package col

import (
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/meta"
)

// TestUser 用于 col 包测试（字段全包装）。
type TestUser struct {
	ID   Col[int64]
	Name Col[string]
	Age  Col[int]
}

func TestSetGet(t *testing.T) {
	var c Col[int]
	if c.IsSet() {
		t.Error("zero Col should not be set")
	}
	c.Set(42)
	if !c.IsSet() {
		t.Error("should be set after Set")
	}
	if got := c.Get(); got != 42 {
		t.Errorf("got %v, want 42", got)
	}
}

// TestResetClearsDirty 验证 Reset 清除 set 标志但保留值。
func TestResetClearsDirty(t *testing.T) {
	var c Col[int]
	c.Set(42)
	if !c.IsSet() {
		t.Fatal("should be set after Set")
	}
	c.Reset()
	if c.IsSet() {
		t.Error("IsSet should be false after Reset")
	}
	// 值保留
	if got := c.Get(); got != 42 {
		t.Errorf("value should be preserved after Reset, got %v", got)
	}
}

func TestMetaFill(t *testing.T) {
	// 注册模型，反射应填充 Col 的列名
	tab := meta.Register[TestUser]("test_users")
	if tab.Meta.Table != "test_users" {
		t.Errorf("table got %q", tab.Meta.Table)
	}
	if got := tab.Meta.FieldByName("Name"); got == nil || got.Column != "name" {
		t.Errorf("Name column got %v", got)
	}
	if got := tab.Meta.FieldByName("ID"); got == nil || got.Column != "id" {
		t.Errorf("ID column got %v", got)
	}
	// 原型实例的 Col 应携带列名
	if tab.Proto.Name.col != "name" {
		t.Errorf("proto Name.col got %q, want name", tab.Proto.Name.col)
	}
}

func TestSnakeColumn(t *testing.T) {
	type T struct {
		CreatedAt Col[string]
	}
	tab := meta.Register[T]("t")
	if got := tab.Meta.FieldByName("CreatedAt").Column; got != "created_at" {
		t.Errorf("got %q, want created_at", got)
	}
}

// TestCompareExprs 验证比较方法生成的 Expr（列名由 renderer quote）
func TestCompareExprs(t *testing.T) {
	tab := meta.Register[TestUser]("test_users")
	// 列引用现在是稳定的 表名.列名（注册时确定，无 SetTableAlias）
	e := tab.Proto.Name.Eq("alice")
	s := renderExpr(e)
	want := "`test_users`.`name` = ?"
	if s != want {
		t.Errorf("Eq got %q, want %q", s, want)
	}
}

// 内部 mini renderer
type miniR struct{ n int }

func (m *miniR) NextPlaceholder() string { m.n++; return "?" }
func (m *miniR) AddParam(any)            {}
func (m *miniR) QuoteCol(tc string) string {
	out := ""
	for i, p := range splitTc(tc) {
		if i > 0 {
			out += "."
		}
		out += "`" + p + "`"
	}
	return out
}
func splitTc(s string) []string {
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
func renderExpr(e expr.Expr) string {
	return e.Render(&miniR{})
}

// TestValueDriver 验证 driver.Valuer（非指针类型）
func TestValueDriver(t *testing.T) {
	var c Col[int]
	c.Set(42)
	v, err := c.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v.(driver.Value) != int64(42) {
		t.Errorf("got %v", v)
	}
}

// TestScanFill 验证 sql.Scanner 扫描
func TestScanFill(t *testing.T) {
	var c Col[int64]
	if err := c.Scan(int64(7)); err != nil {
		t.Fatal(err)
	}
	if c.Get() != 7 {
		t.Errorf("got %v, want 7", c.Get())
	}
	// 扫描不置 set 标志（见 #3 陷阱1）
	if c.IsSet() {
		t.Error("scan should not set the set flag")
	}
}

// TestScanNull 验证 NULL 扫描
func TestScanNull(t *testing.T) {
	var c Col[int64]
	if err := c.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if c.Get() != 0 {
		t.Errorf("got %v, want 0", c.Get())
	}
}

// TestPtrNullCol 验证 Col[*T] 的 NULL 语义（nil = NULL，见 #3）
func TestPtrNullCol(t *testing.T) {
	var c Col[*string]
	v, err := c.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("nil ptr should be NULL, got %v", v)
	}
	// 扫描 NULL → nil
	if err := c.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if c.Get() != nil {
		t.Error("should stay nil")
	}
	// 扫描值 → 解引用
	if err := c.Scan("bob"); err != nil {
		t.Fatal(err)
	}
	if c.Get() == nil || *c.Get() != "bob" {
		t.Errorf("got %v, want *bob", c.Get())
	}
}

// TestJSONTransparent 验证 JSON 透明序列化（决策1）
func TestJSONTransparent(t *testing.T) {
	type Wrap struct {
		Name Col[string]
		Age  Col[int]
	}
	w := Wrap{}
	w.Name.Set("alice")
	w.Age.Set(20)

	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	// 应输出原生形态 {"Name":"alice","Age":20}
	want := `{"Name":"alice","Age":20}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}

	// 反序列化
	var w2 Wrap
	if err := json.Unmarshal(b, &w2); err != nil {
		t.Fatal(err)
	}
	if w2.Name.Get() != "alice" {
		t.Errorf("name got %v", w2.Name.Get())
	}
	if w2.Age.Get() != 20 {
		t.Errorf("age got %v", w2.Age.Get())
	}
}

// TestRegisterCached 验证重复注册返回缓存
func TestRegisterCached(t *testing.T) {
	t1 := meta.Register[TestUser]("test_users")
	t2 := meta.Register[TestUser]("") // 不同表名参数
	if t1 != t2 {
		t.Error("repeated Register should return cached table")
	}
}
