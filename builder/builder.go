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
	d        dialect.Dialect
	phIdx    int               // 占位符计数器
	args     []any             // 收集的参数
	aliasMap map[string]string // 表名→别名（如 users→u）；表名不在 map 中则原样保留
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
		// 表名无别名映射：只输出列名（单表场景）
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
}

// BuildSELECT 生成 SELECT 语句的 (SQL, args)。
// m 提供列名列表（仅当 SelectCols 为空时用于整表投影）；d 提供方言。
// q.Alias 主表别名 + q.Joins 各连接表别名 → 构造 表名→别名 映射供 renderer 替换。
func BuildSELECT(m *meta.ModelMeta, q SelectQuery, d dialect.Dialect) (string, []any) {
	// 构造 表名→别名 映射
	aliasMap := map[string]string{}
	if q.Alias != "" {
		aliasMap[m.Table] = q.Alias // 主表
	}
	for _, j := range q.Joins {
		if j.Alias != "" {
			aliasMap[j.Table] = j.Alias
		}
	}
	r := &renderer{d: d, aliasMap: aliasMap}

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
	sql := prefix + strings.Join(colParts, ", ") + " FROM " + d.QuoteTable(m.Table)
	if q.Alias != "" {
		sql += " AS " + d.QuoteIdent(q.Alias)
	}

	// JOIN
	for _, j := range q.Joins {
		sql += " " + j.Kind + " JOIN " + d.QuoteTable(j.Table)
		if j.Alias != "" {
			sql += " AS " + d.QuoteIdent(j.Alias)
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
		sql += " LIMIT " + d.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, q.Limit)
	}
	if q.Offset > 0 {
		sql += " OFFSET " + d.Placeholder(r.phIdx+1)
		r.phIdx++
		r.args = append(r.args, q.Offset)
	}

	return sql, r.args
}
