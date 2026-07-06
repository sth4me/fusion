// Package builder 把 Query 配置 + Expr 渲染成最终的 (SQL, args)。
//
// builder 实现 expr.Renderer（占位符/列引用/参数收集），并组装 SELECT 语句。
//
// 详见 docs/DESIGN.md 决策 1、3、4。
package builder

import (
	"strings"

	"fusion/dialect"
	"fusion/expr"
	"fusion/meta"
)

// renderer 是 expr.Renderer 的实现，绑定方言与表名→别名映射。
// 列引用形如 "表名.列名"（稳定，注册时确定），render 时表名按 aliasMap 替换为别名。
// 若表名不在 aliasMap 中（如单表 DML），则去掉表前缀只输出列名。
// 这样 Expr 完全不可变，别名仅在 render 时解析，并发安全。
type renderer struct {
	d          dialect.Dialect
	phIdx      int               // 占位符计数器
	args       []any             // 收集的参数
	aliasMap   map[string]string // 表名→别名（如 users→u）
	keepPrefix bool              // true=保留未知表前缀（子查询引用外层表用）
}

func (r *renderer) NextPlaceholder() string {
	r.phIdx++
	return r.d.Placeholder(r.phIdx)
}

func (r *renderer) AddParam(v any) { r.args = append(r.args, v) }

// QuoteCol 引用列引用。输入形如 "表名.列名" 或 "列名"。
//   - 表名在 aliasMap 中：替换为别名（如 users.name → u.name）
//   - 表名不在 aliasMap 中（单表场景）：去掉表前缀，只输出列名（避免 users.id 在单表 UPDATE 报错）
func (r *renderer) QuoteCol(tableCol string) string {
	if i := strings.IndexByte(tableCol, '.'); i >= 0 {
		left := tableCol[:i]
		col := tableCol[i+1:]
		if alias, ok := r.aliasMap[left]; ok {
			return r.d.QuoteIdent(alias) + "." + r.d.QuoteIdent(col)
		}
		// 表名无别名映射
		if r.keepPrefix {
			// 子查询场景：保留表前缀（引用外层表）
			return r.d.QuoteIdent(left) + "." + r.d.QuoteIdent(col)
		}
		// 单表场景：去前缀只输出列名
		return r.d.QuoteIdent(col)
	}
	return r.d.QuoteIdent(tableCol)
}

// Args 返回已收集的参数。
func (r *renderer) Args() []any { return r.args }

// OrderItem 是排序子句项的最小接口。col.Order 实现它。
type OrderItem interface {
	RenderClause(r expr.Renderer) string
}

// SelectItem 是投影项接口。col.SelectItem 实现它。
type SelectItem interface {
	RenderSelect(r expr.Renderer) string
}

// JoinSpec 描述一个 JOIN 子句。
type JoinSpec struct {
	Kind  string    // "INNER"/"LEFT"/"RIGHT"/"FULL"
	Table string    // 被连接表名
	Alias string    // 表别名
	On    expr.Expr // ON 条件（EqCol 组合）
}

// GroupItem 是 GROUP BY 项接口（col.Col 实现 RenderClause）。
type GroupItem interface {
	RenderClause(r expr.Renderer) string
}

// SelectQuery 描述一个 SELECT 查询的配置。
type SelectQuery struct {
	Table      string        // 主表名
	Alias      string        // 主表别名
	SelectCols []SelectItem  // 投影列（空则整表所有列，向后兼容）
	Joins      []JoinSpec    // JOIN 子句
	Where      expr.Expr
	GroupBy    []GroupItem   // GROUP BY 项
	Having     expr.Expr
	Orders     []OrderItem   // 排序子句
	Distinct   bool
	Limit      int
	Offset     int
	LockClause string        // 锁子句（"FOR UPDATE"/"FOR SHARE"/"FOR UPDATE NOWAIT"等，空=无锁）
	CTEs       []CTESpec     // WITH 子句（CTE，含递归）；渲染在 SELECT 之前
}

// CTESpec 描述一个 CTE（公用表表达式）。
//
// 实现策略：CTE 体用原始 SQL + 参数（SQL 原样拼入，参数并入外层 renderer）。
// 原因：CTE 内部可能 FROM 一个 CTE 名（无对应注册模型），无法走模型驱动的 builder；
// 原始 SQL 是最灵活且与 Raw 兜底哲学一致的方式。
type CTESpec struct {
	Name      string   // CTE 名（被主查询/递归引用）
	Recursive bool     // 是否递归（WITH RECURSIVE）
	Columns   []string // 可选：CTE 列名列表（CTE 名后的 (col1, col2)）
	SQL       string   // CTE 体的 SQL（不含外层括号；占位符用 ?，渲染时重写为外层序号）
	Args      []any    // CTE 体的参数（与 SQL 中 ? 一一对应）
}

// BuildSubquerySQL 生成子查询 SQL（keepPrefix=true，保留外层表引用）。
// 供 EXISTS/IN 子查询用。
func BuildSubquerySQL(m *meta.ModelMeta, q SelectQuery, d dialect.Dialect) (string, []any) {
	aliasMap := map[string]string{}
	if q.Alias != "" {
		aliasMap[m.Table] = q.Alias
	}
	for _, j := range q.Joins {
		if j.Alias != "" {
			aliasMap[j.Table] = j.Alias
		}
	}
	r := &renderer{d: d, aliasMap: aliasMap, keepPrefix: true}
	return buildSelectBody(r, m, q, d), r.args
}

// BuildSELECT 生成 SELECT 语句的 (SQL, args)。
// m 提供列名列表（仅当 SelectCols 为空时用于整表投影）；d 提供方言。
// q.Alias 主表别名 + q.Joins 各连接表别名 → 构造 表名→别名 映射供 renderer 替换。
func BuildSELECT(m *meta.ModelMeta, q SelectQuery, d dialect.Dialect) (string, []any) {
	aliasMap := map[string]string{}
	if q.Alias != "" {
		aliasMap[m.Table] = q.Alias
	}
	for _, j := range q.Joins {
		if j.Alias != "" {
			aliasMap[j.Table] = j.Alias
		}
	}
	r := &renderer{d: d, aliasMap: aliasMap}
	return buildSelectBody(r, m, q, d), r.args
}

// buildSelectBody 用给定 renderer 生成 SELECT 语句主体（不含参数返回，参数在 r.args）。
func buildSelectBody(r *renderer, m *meta.ModelMeta, q SelectQuery, d dialect.Dialect) string {
	// renderer 已绑定方言（r.d），统一用它（兼容 d 参数为 nil 的子查询场景）
	di := r.d

	// WITH（CTE）：渲染在 SELECT 之前；CTE 参数先并入 renderer，保证占位符序号连续。
	var sql string
	if len(q.CTEs) > 0 {
		sql = renderCTEs(r, q.CTEs) + " "
	}

	// SELECT 列：有 SelectCols 用投影项，否则整表所有列（向后兼容）
	var colParts []string
	if len(q.SelectCols) > 0 {
		colParts = make([]string, 0, len(q.SelectCols))
		for _, item := range q.SelectCols {
			colParts = append(colParts, item.RenderSelect(r))
		}
	} else {
		colParts = make([]string, 0, len(m.Fields))
		for _, f := range m.Fields {
			if f.IsRelation {
				continue
			}
			// 用稳定的 表名.列名，renderer 的 QuoteCol 会按 aliasMap 替换表名为别名
			colParts = append(colParts, r.QuoteCol(f.Table+"."+f.Column))
		}
	}

	prefix := "SELECT "
	if q.Distinct {
		prefix = "SELECT DISTINCT "
	}
	sql += prefix + strings.Join(colParts, ", ") + " FROM " + di.QuoteTable(m.Table)
	if q.Alias != "" {
		sql += " AS " + di.QuoteIdent(q.Alias)
	}

	// JOIN
	for _, j := range q.Joins {
		sql += " " + j.Kind + " JOIN " + di.QuoteTable(j.Table)
		if j.Alias != "" {
			sql += " AS " + di.QuoteIdent(j.Alias)
		}
		if !j.On.IsZero() {
			sql += " ON " + j.On.Render(r)
		}
	}

	// WHERE
	if !q.Where.IsZero() {
		where := q.Where.Render(r)
		if where != "" {
			sql += " WHERE " + where
		}
	}

	// GROUP BY
	if len(q.GroupBy) > 0 {
		parts := make([]string, 0, len(q.GroupBy))
		for _, g := range q.GroupBy {
			parts = append(parts, g.RenderClause(r))
		}
		sql += " GROUP BY " + strings.Join(parts, ", ")
	}

	// HAVING
	if !q.Having.IsZero() {
		having := q.Having.Render(r)
		if having != "" {
			sql += " HAVING " + having
		}
	}

	// ORDER BY
	if len(q.Orders) > 0 {
		parts := make([]string, 0, len(q.Orders))
		for _, o := range q.Orders {
			parts = append(parts, o.RenderClause(r))
		}
		sql += " ORDER BY " + strings.Join(parts, ", ")
	}

	// LIMIT / OFFSET
	if q.Limit > 0 {
		sql += " LIMIT " + di.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, q.Limit)
	}
	if q.Offset > 0 {
		sql += " OFFSET " + di.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, q.Offset)
	}

	// 锁子句（FOR UPDATE/FOR SHARE，在 LIMIT 之后）
	if q.LockClause != "" {
		sql += " " + q.LockClause
	}

	return sql
}

// renderCTEs 渲染 WITH 子句（含递归）：`WITH [RECURSIVE] name[(cols)] AS (body), ...`。
// 每个 CTE body 中的 ? 占位符按出现顺序重写为 renderer 的编号（参数并入 r.args）。
// 不含末尾空格；调用方负责拼接。
func renderCTEs(r *renderer, ctes []CTESpec) string {
	recursive := false
	for _, c := range ctes {
		if c.Recursive {
			recursive = true
			break
		}
	}
	prefix := "WITH "
	if recursive {
		prefix = "WITH RECURSIVE "
	}
	parts := make([]string, 0, len(ctes))
	for _, c := range ctes {
		body := rewritePlaceholdersInto(r, c.SQL, c.Args)
		var s string
		if len(c.Columns) > 0 {
			cols := make([]string, len(c.Columns))
			for i, col := range c.Columns {
				cols[i] = r.d.QuoteIdent(col)
			}
			s = r.d.QuoteIdent(c.Name) + " (" + strings.Join(cols, ", ") + ") AS (" + body + ")"
		} else {
			s = r.d.QuoteIdent(c.Name) + " AS (" + body + ")"
		}
		parts = append(parts, s)
	}
	return prefix + strings.Join(parts, ", ")
}

// rewritePlaceholdersInto 把 SQL 中的占位符（? 或 $N）按出现顺序重写为 renderer 的编号，
// 并把对应 args 依次 AddParam。返回重写后的 SQL。
func rewritePlaceholdersInto(r *renderer, sqlStr string, args []any) string {
	var out strings.Builder
	out.Grow(len(sqlStr))
	argIdx := 0
	i := 0
	for i < len(sqlStr) {
		ch := sqlStr[i]
		if ch == '?' {
			if argIdx < len(args) {
				r.AddParam(args[argIdx])
				argIdx++
			}
			out.WriteString(r.NextPlaceholder())
			i++
			continue
		}
		if ch == '$' && i+1 < len(sqlStr) && sqlStr[i+1] >= '1' && sqlStr[i+1] <= '9' {
			// $N 形式：跳过数字，用 renderer 编号替换
			j := i + 1
			for j < len(sqlStr) && sqlStr[j] >= '0' && sqlStr[j] <= '9' {
				j++
			}
			if argIdx < len(args) {
				r.AddParam(args[argIdx])
				argIdx++
			}
			out.WriteString(r.NextPlaceholder())
			i = j
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}
