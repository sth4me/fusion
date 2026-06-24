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

// renderer 是 expr.Renderer 的实现，绑定方言与表别名。
type renderer struct {
	d     dialect.Dialect
	phIdx int      // 占位符计数器
	args  []any    // 收集的参数
	alias string   // 当前主表别名
}

func (r *renderer) NextPlaceholder() string {
	r.phIdx++
	return r.d.Placeholder(r.phIdx)
}

func (r *renderer) AddParam(v any) { r.args = append(r.args, v) }

// QuoteCol 引用列引用（"t0.name" → "t0"."name"），按方言分别 quote。
func (r *renderer) QuoteCol(tableCol string) string {
	if i := strings.IndexByte(tableCol, '.'); i >= 0 {
		return r.d.QuoteIdent(tableCol[:i]) + "." + r.d.QuoteIdent(tableCol[i+1:])
	}
	return r.d.QuoteIdent(tableCol)
}

// Args 返回已收集的参数。
func (r *renderer) Args() []any { return r.args }

// OrderItem 是排序子句项的最小接口。col.Order 实现它。
type OrderItem interface {
	RenderClause(r expr.Renderer) string
}

// SelectQuery 描述一个 SELECT 查询的配置。
type SelectQuery struct {
	Table  string     // 表名
	Alias  string     // 表别名（默认无别名，用列名）
	Where  expr.Expr
	Orders []OrderItem // 排序子句（col.Asc()/Desc() 返回的 Order）
	Limit  int
	Offset int
}

// BuildSELECT 生成 SELECT 语句的 (SQL, args)。
// m 提供列名列表，d 提供方言。
func BuildSELECT(m *meta.ModelMeta, q SelectQuery, d dialect.Dialect) (string, []any) {
	r := &renderer{d: d, alias: q.Alias}

	// SELECT 列：所有字段的表别名.列名
	colParts := make([]string, 0, len(m.Fields))
	for _, f := range m.Fields {
		ref := f.Column
		if q.Alias != "" {
			ref = q.Alias + "." + f.Column
		}
		colParts = append(colParts, r.QuoteCol(ref))
	}
	sql := "SELECT " + strings.Join(colParts, ", ") + " FROM " + d.QuoteTable(m.Table)
	if q.Alias != "" {
		sql += " AS " + d.QuoteIdent(q.Alias)
	}

	// WHERE
	if !q.Where.IsZero() {
		where := q.Where.Render(r)
		if where != "" {
			sql += " WHERE " + where
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
