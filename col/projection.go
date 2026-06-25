package col

import "fusion/expr"

// SelectItem 表示 SELECT 列表的一个投影项（普通列或聚合函数）。
// 由 Col.As() 或聚合函数（Count/Sum/Avg/Min/Max）产生。
type SelectItem struct {
	// 普通列：colRef 存 table.col，isAgg=false
	// 聚合：funcName 存函数名（SUM/AVG/...），colRef 存内部列引用或 "*"，isAgg=true
	colRef   string // 列引用原始形式（table.col）或 "*"
	isAgg    bool   // 是否聚合
	funcName string // 聚合函数名（COUNT/SUM/AVG/MIN/MAX）
	as       string // AS 别名（可空）
}

// As 设置投影别名（SELECT ... AS alias）。
func (s SelectItem) As(alias string) SelectItem {
	s.as = alias
	return s
}

// Alias 返回 AS 别名。
func (s SelectItem) Alias() string { return s.as }

// RenderSelect 按 renderer 生成完整投影 SQL（col AS alias）。
func (s SelectItem) RenderSelect(d expr.Renderer) string {
	var out string
	if s.isAgg {
		inner := s.colRef
		if inner != "*" {
			inner = d.QuoteCol(inner)
		}
		out = s.funcName + "(" + inner + ")"
	} else {
		out = d.QuoteCol(s.colRef)
	}
	if s.as != "" {
		out += " AS " + s.as
	}
	return out
}

// RenderClause 实现 builder.OrderItem（聚合/列可用于 ORDER BY，无 AS）。
func (s SelectItem) RenderClause(d expr.Renderer) string {
	if s.isAgg {
		inner := s.colRef
		if inner != "*" {
			inner = d.QuoteCol(inner)
		}
		return s.funcName + "(" + inner + ")"
	}
	return d.QuoteCol(s.colRef)
}

// AggOrder 是聚合排序项（携带方向），实现 builder.OrderItem。
type AggOrder struct {
	item SelectItem
	dir  string
}

// Asc 聚合升序（如 ORDER BY COUNT(*) ASC）。
func (s SelectItem) Asc() AggOrder { return AggOrder{item: s, dir: "ASC"} }

// Desc 聚合降序（如 ORDER BY COUNT(*) DESC）。
func (s SelectItem) Desc() AggOrder { return AggOrder{item: s, dir: "DESC"} }

// RenderClause 实现 builder.OrderItem。
func (a AggOrder) RenderClause(d expr.Renderer) string {
	return a.item.RenderClause(d) + " " + a.dir
}

// Col.As 生成普通列投影。
// alias 为 AS 别名（应与投影结构体字段的 db tag 或蛇形名对齐）。
func (c Col[T]) As(alias string) SelectItem {
	return SelectItem{colRef: c.ref(), as: alias}
}

// --- 聚合函数（返回 SelectItem，可 .As()）---

// Count 生成 COUNT 聚合。无参时 COUNT(*)，传 Col 时 COUNT(col)。
func Count[T any](c ...Col[T]) SelectItem {
	if len(c) == 0 {
		return SelectItem{isAgg: true, funcName: "COUNT", colRef: "*"}
	}
	return SelectItem{isAgg: true, funcName: "COUNT", colRef: c[0].ref()}
}

// Sum 生成 SUM(col)。
func Sum[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "SUM", colRef: c.ref()}
}

// Avg 生成 AVG(col)。
func Avg[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "AVG", colRef: c.ref()}
}

// Min 生成 MIN(col)。
func Min[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "MIN", colRef: c.ref()}
}

// Max 生成 MAX(col)。
func Max[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "MAX", colRef: c.ref()}
}

// --- 聚合比较（用于 HAVING）---
// 用 expr.LeafRawSQL 构造（聚合函数不 quote，直接作为左操作数）。

// CountGt 生成 HAVING COUNT(*) > n。
func CountGt(n int64) expr.Expr { return expr.LeafRawSQL("COUNT(*)", ">", n) }

// CountGte 生成 HAVING COUNT(*) >= n。
func CountGte(n int64) expr.Expr { return expr.LeafRawSQL("COUNT(*)", ">=", n) }

// CountLt 生成 HAVING COUNT(*) < n。
func CountLt(n int64) expr.Expr { return expr.LeafRawSQL("COUNT(*)", "<", n) }
