package expr

import "testing"

// fakeRenderer 用于测试：占位符用 ?（MySQL 风格），收集参数，列引用用反引号。
type fakeRenderer struct {
	n     int
	args  []any
}

func (f *fakeRenderer) NextPlaceholder() string { f.n++; return "?" }
func (f *fakeRenderer) AddParam(v any)          { f.args = append(f.args, v) }

func (f *fakeRenderer) QuoteCol(tableCol string) string {
	// 简化：点分隔部分各自用反引号包裹
	parts := splitDot(tableCol)
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "."
		}
		out += "`" + p + "`"
	}
	return out
}

func splitDot(s string) []string {
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
	if cur != "" || len(out) > 0 {
		out = append(out, cur)
	}
	return out
}

func render(e Expr) (string, []any) {
	r := &fakeRenderer{}
	s := e.Render(r)
	return s, r.args
}

func TestLeafSingle(t *testing.T) {
	s, args := render(LeafParam("name", "=", "alice"))
	want := "`name` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args got %v, want [alice]", args)
	}
}

func TestLeafIn(t *testing.T) {
	s, args := render(LeafMulti("id", "IN", []any{1, 2, 3}))
	want := "`id` IN (?, ?, ?)"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
	if len(args) != 3 {
		t.Errorf("args got %v, want 3", args)
	}
}

func TestLeafIsNull(t *testing.T) {
	s, args := render(LeafRaw("deleted_at", "IS NULL"))
	want := "`deleted_at` IS NULL"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
	if len(args) != 0 {
		t.Errorf("args got %v, want none", args)
	}
}

// TestFlatSameKind 验证同类扁平化：A AND B AND C → 不加括号
func TestFlatSameKind(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	c := LeafParam("c", "=", 3)
	s, _ := render(a.And(b).And(c))
	want := "`a` = ? AND `b` = ? AND `c` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestCrossKindParens 验证跨类括号：Or{And{A,B}, C} → (A AND B) OR C
func TestCrossKindParens(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	c := LeafParam("c", "=", 3)
	s, _ := render(a.And(b).Or(c))
	want := "(`a` = ? AND `b` = ?) OR `c` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestNestedCross 验证 And{A, Or{B,C}} → A AND (B OR C)
func TestNestedCross(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	c := LeafParam("c", "=", 3)
	s, _ := render(a.And(b.Or(c)))
	want := "`a` = ? AND (`b` = ? OR `c` = ?)"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestNotLeaf 验证 NOT 叶节点不加括号：NOT a = ?
func TestNotLeaf(t *testing.T) {
	a := LeafParam("a", "=", 1)
	s, _ := render(a.Not())
	want := "NOT `a` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestNotComposite 验证 NOT 组合节点加括号：NOT (a = ? AND b = ?)
func TestNotComposite(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	s, _ := render(a.And(b).Not())
	want := "NOT (`a` = ? AND `b` = ?)"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestComplexExample 文档 #1 的核心验证用例
// A.And(B).Or(C.And(Not(D))).Or(E)
// → (A AND B) OR (C AND NOT D) OR E
func TestComplexExample(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	c := LeafParam("c", "=", 3)
	d := LeafParam("d", "=", 4)
	e := LeafParam("e", "=", 5)
	s, _ := render(a.And(b).Or(c.And(d.Not())).Or(e))
	want := "(`a` = ? AND `b` = ?) OR (`c` = ? AND NOT `d` = ?) OR `e` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestDeepSameKindFlat 验证深度同类扁平：A AND (B AND (C AND D))
// 用户显式嵌套，但同类 → 扁平
func TestDeepSameKindFlat(t *testing.T) {
	a := LeafParam("a", "=", 1)
	b := LeafParam("b", "=", 2)
	c := LeafParam("c", "=", 3)
	d := LeafParam("d", "=", 4)
	s, _ := render(a.And(b.And(c.And(d))))
	want := "`a` = ? AND `b` = ? AND `c` = ? AND `d` = ?"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
}

// TestParamOrder 验证参数收集顺序遵循树遍历
func TestParamOrder(t *testing.T) {
	a := LeafParam("a", "=", "x")
	b := LeafParam("b", "=", "y")
	c := LeafParam("c", "=", "z")
	_, args := render(a.And(b).Or(c))
	want := []any{"x", "y", "z"}
	if len(args) != len(want) {
		t.Fatalf("args got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] got %v, want %v", i, args[i], want[i])
		}
	}
}

// TestColCol 列对列比较（JOIN ON）
func TestColCol(t *testing.T) {
	s, args := render(LeafColCol("t0.dept_id", "=", "t1.id"))
	want := "`t0`.`dept_id` = `t1`.`id`"
	if s != want {
		t.Errorf("got %q, want %q", s, want)
	}
	if len(args) != 0 {
		t.Errorf("args got %v, want none", args)
	}
}
