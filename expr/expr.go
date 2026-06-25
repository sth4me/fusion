// Package expr 实现类型安全的 SQL 表达式树。
//
// 表达式用节点树（Leaf/And/Or/Not）表示，render 时采用「同类扁平 + 跨类括号」
// 策略：同类（Or/Or、And/And）扁平化不加括号（结合律保证等价）；跨类一律加括号，
// 因此完全不依赖 SQL 默认运算符优先级，用户构建时无需背诵 AND>OR。
//
// 详见 docs/DESIGN.md #1。
package expr

// nodeKind 标识节点类别，用于 render 判定同类/跨类。
type nodeKind int

const (
	kindLeaf nodeKind = iota // 叶节点（比较、IS NULL 等）
	kindAnd
	kindOr
	kindNot
)

// nodeImpl 是所有节点类型的公共接口。
type nodeImpl interface {
	kind() nodeKind
	// render 生成 SQL 片段。parentKind 为父节点类别（根节点为 kindLeaf）。
	render(parentKind nodeKind, d Renderer) string
}

// Renderer 抽象 SQL 占位符生成、列引用与参数收集。
// 由 builder 提供：render 过程中按树遍历顺序收集参数、生成占位符、引用列名。
type Renderer interface {
	// NextPlaceholder 返回下一个占位符（PostgreSQL $1/$2/…，MySQL 固定 ?），
	// 内部维护序号，每次调用递增。
	NextPlaceholder() string
	// AddParam 收集一个参数值。
	AddParam(v any)
	// QuoteCol 引用一个列引用表达式（可能含表别名前缀 "t0.name"），
	// 按各方言分别 quote（PG/SQLite: "t0"."name"；MySQL: `t0`.`name`）。
	QuoteCol(tableCol string) string
}

// paramCollector 由 builder 实现，render 时按遍历顺序收集参数。
// （已合并进 Renderer，保留接口名供旧代码引用兼容。）
type paramCollector = Renderer

// Expr 是不可变的布尔表达式。零值表示空条件，请用构造函数创建。
type Expr struct{ n nodeImpl }

// IsZero 报告 e 是否为空表达式（未设置条件）。
func (e Expr) IsZero() bool { return e.n == nil }

// And 将 e 与 others 用 AND 连接，返回新表达式。
func (e Expr) And(others ...Expr) Expr {
	if e.n == nil {
		return join(kindAnd, others)
	}
	all := make([]Expr, 0, 1+len(others))
	all = append(all, e)
	for _, o := range others {
		if o.n != nil {
			all = append(all, o)
		}
	}
	return join(kindAnd, all)
}

// Or 将 e 与 others 用 OR 连接，返回新表达式。
func (e Expr) Or(others ...Expr) Expr {
	if e.n == nil {
		return join(kindOr, others)
	}
	all := make([]Expr, 0, 1+len(others))
	all = append(all, e)
	for _, o := range others {
		if o.n != nil {
			all = append(all, o)
		}
	}
	return join(kindOr, all)
}

// Not 对 e 取反，返回 NOT (e)。
func (e Expr) Not() Expr {
	if e.n == nil {
		return e
	}
	return Expr{n: notNode{child: e}}
}

// Node 返回内部节点，供 builder 调用 render。
func (e Expr) Node() nodeImpl { return e.n }

// join 把同层同类的子表达式合并为一个复合节点。
func join(k nodeKind, children []Expr) Expr {
	nonNil := make([]Expr, 0, len(children))
	for _, c := range children {
		if c.n != nil {
			nonNil = append(nonNil, c)
		}
	}
	switch len(nonNil) {
	case 0:
		return Expr{}
	case 1:
		return nonNil[0]
	default:
		return Expr{n: composite{kindVal: k, children: nonNil}}
	}
}

// composite 是 And/Or 的统一载体。
type composite struct {
	kindVal  nodeKind
	children []Expr
}

func (c composite) kind() nodeKind { return c.kindVal }

func (c composite) render(parentKind nodeKind, d Renderer) string {
	op := " AND "
	if c.kindVal == kindOr {
		op = " OR "
	}
	parts := make([]string, 0, len(c.children))
	for _, child := range c.children {
		parts = append(parts, child.n.render(c.kindVal, d))
	}
	body := joinStrings(parts, op)
	// 与父节点同类 → 不加括号（同层扁平）；跨类 → 加括号；根节点不加括号。
	if c.kindVal != parentKind && parentKind != kindLeaf {
		return "(" + body + ")"
	}
	return body
}

// notNode 是 NOT 的载体。
type notNode struct{ child Expr }

func (n notNode) kind() nodeKind { return kindNot }

func (n notNode) render(_ nodeKind, d Renderer) string {
	ck := n.child.n.kind()
	// 传 kindLeaf 让子节点不再因"跨类"自行加括号——NOT 统一负责括号。
	inner := n.child.n.render(kindLeaf, d)
	// NOT 的操作数：叶节点不加括号（NOT x）；组合节点加括号（NOT (a AND b)）。
	if ck == kindLeaf {
		return "NOT " + inner
	}
	return "NOT (" + inner + ")"
}

// leafSpec 描述一个叶比较的各部分，render 时由 Renderer 生成占位符。
// 例如 col=name, op="=", param="alice" → "name = $1"，参数收集 "alice"。
type leafSpec struct {
	col   string // 列名（已含表别名前缀，如 t0.name）
	op    string // 运算符：=, >, IS NULL, IN, ...
	param any    // 参数值；op 为 IS NULL/IS NOT NULL 时为 nil 且不收集
	multi []any  // IN 等多值运算符的参数列表；非空时 param 忽略
}

// LeafParam 用列名、运算符、单个参数构造一个叶节点。
// 供 col 包使用：col 已算好列名，op 是运算符，param 是比较值。
func LeafParam(col, op string, param any) Expr {
	return Expr{n: leafNode{s: leafSpec{col: col, op: op, param: param}}}
}

// LeafRawSQL 用原始左操作数（不 quote，如聚合函数 COUNT(*)）、运算符、参数构造叶节点。
// 用于 HAVING 聚合比较（聚合函数不该被当列名引用）。
func LeafRawSQL(leftExpr, op string, param any) Expr {
	return Expr{n: rawLeafNode{left: leftExpr, op: op, param: param}}
}

// LeafBetween 生成 BETWEEN 表达式（col BETWEEN $1 AND $2）。
func LeafBetween(col string, lo, hi any) Expr {
	return Expr{n: betweenNode{col: col, lo: lo, hi: hi}}
}

// rawLeafNode 是原始左操作数的叶节点（不 quote，用于聚合函数）。
type rawLeafNode struct {
	left  string // 原始 SQL 片段（如 "COUNT(*)"），不 quote
	op    string
	param any
}

// betweenNode 是 BETWEEN 的载体。
type betweenNode struct {
	col string
	lo  any
	hi  any
}

func (betweenNode) kind() nodeKind { return kindLeaf }
func (b betweenNode) render(_ nodeKind, d Renderer) string {
	d.AddParam(b.lo)
	d.AddParam(b.hi)
	return d.QuoteCol(b.col) + " BETWEEN " + d.NextPlaceholder() + " AND " + d.NextPlaceholder()
}

func (rawLeafNode) kind() nodeKind { return kindLeaf }
func (r rawLeafNode) render(_ nodeKind, d Renderer) string {
	d.AddParam(r.param)
	return r.left + " " + r.op + " " + d.NextPlaceholder()
}

// LeafMulti 用列名、运算符、多值参数构造叶节点（用于 IN）。
func LeafMulti(col, op string, params []any) Expr {
	return Expr{n: leafNode{s: leafSpec{col: col, op: op, multi: params}}}
}

// LeafRaw 用列名、运算符构造无参叶节点（用于 IS NULL / IS NOT NULL）。
func LeafRaw(col, op string) Expr {
	return Expr{n: leafNode{s: leafSpec{col: col, op: op}}}
}

// LeafColCol 用于列与列比较（JOIN ON），如 "t0.dept_id = t1.id"，无参数。
func LeafColCol(leftCol, op, rightCol string) Expr {
	return Expr{n: leafNode{s: leafSpec{col: leftCol, op: op, param: rightCol, multi: []any{nil}}, colCol: true, rightCol: rightCol}}
}

type leafNode struct {
	s     leafSpec
	colCol bool // 列对列比较（无占位符）
	rightCol string
}

func (l leafNode) kind() nodeKind { return kindLeaf }

func (l leafNode) render(_ nodeKind, d Renderer) string {
	col := d.QuoteCol(l.s.col)
	if l.colCol {
		return col + " " + l.s.op + " " + d.QuoteCol(l.rightCol)
	}
	if l.s.op == "IS NULL" || l.s.op == "IS NOT NULL" {
		return col + " " + l.s.op
	}
	if len(l.s.multi) > 0 {
		// IN (?, ?, ?)
		ph := make([]string, len(l.s.multi))
		for i, v := range l.s.multi {
			d.AddParam(v)
			ph[i] = d.NextPlaceholder()
		}
		return col + " " + l.s.op + " (" + joinStrings(ph, ", ") + ")"
	}
	// 单值比较
	d.AddParam(l.s.param)
	return col + " " + l.s.op + " " + d.NextPlaceholder()
}

// Render 生成最终的 WHERE 子句 SQL（不含 "WHERE" 关键字）。
// d 提供占位符并收集参数。空表达式返回空串。
func (e Expr) Render(d Renderer) string {
	if e.n == nil {
		return ""
	}
	return e.n.render(kindLeaf, d)
}

func joinStrings(parts []string, sep string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}
