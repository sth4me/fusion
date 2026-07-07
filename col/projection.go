package col

import (
	"strings"

	"github.com/sth4me/fusion/expr"
)

// SelectItem 表示 SELECT 列表的一个投影项（普通列或聚合函数）。
// 由 Col.As() 或聚合函数（Count/Sum/Avg/Min/Max）产生。
type SelectItem struct {
	// 普通列：colRef 存 table.col，isAgg=false
	// 聚合：funcName 存函数名（SUM/AVG/...），colRef 存内部列引用或 "*"，isAgg=true
	colRef    string // 列引用原始形式（table.col）或 "*"
	isAgg     bool   // 是否聚合
	funcName  string // 聚合函数名（COUNT/SUM/AVG/MIN/MAX）
	as        string // AS 别名（可空）
	over      string // OVER (...) 子句原文（窗口函数；可空=非窗口）
	rawInner  string // 非空时作为函数体原文（不 quote），用于 LAG(col,2) 等带多参/字面量的窗口函数
}

// As 设置投影别名（SELECT ... AS alias）。
func (s SelectItem) As(alias string) SelectItem {
	s.as = alias
	return s
}

// Alias 返回 AS 别名。
func (s SelectItem) Alias() string { return s.as }

// Over 附加 OVER (...) 窗口子句，把当前项变成窗口函数。
//
// partitionCols / orderCols 为 PARTITION BY / ORDER BY 的列表达式原样字符串
// （调用方负责 quote/方向，例如 `"dept_id"`、`"age DESC"`）。
// 不做方言 quote（避免 col→dialect 循环）；多数场景列名无需 quote。
// 渲染：func(col) OVER (PARTITION BY ... ORDER BY ...) AS alias。
//
// 用法：
//   fusion.Sum(amount).Over([]string{"dept_id"}, []string{"age DESC"}).As("running_total")
//   fusion.RowNumber().Over(nil, []string{"age DESC"}).As("rn")
func (s SelectItem) Over(partitionCols, orderCols []string) SelectItem {
	var b string
	if len(partitionCols) > 0 {
		b += "PARTITION BY " + strings.Join(partitionCols, ", ")
	}
	if len(orderCols) > 0 {
		if b != "" {
			b += " "
		}
		b += "ORDER BY " + strings.Join(orderCols, ", ")
	}
	if b == "" {
		s.over = "OVER ()"
	} else {
		s.over = "OVER (" + b + ")"
	}
	return s
}

// RenderSelect 按 renderer 生成完整投影 SQL（col AS alias）。
func (s SelectItem) RenderSelect(d expr.Renderer) string {
	var out string
	if s.isAgg {
		switch {
		case s.rawInner != "":
			// 带字面量/多参的窗口函数体（如 LAG(col, 2)）：rawInner 含已 quote 的列 + 字面量
			out = s.funcName + "(" + s.rawInner + ")"
		case s.colRef == "":
			// 无参窗口函数：ROW_NUMBER() / RANK() / DENSE_RANK()
			out = s.funcName + "()"
		default:
			inner := s.colRef
			if inner != "*" {
				inner = d.QuoteCol(inner)
			}
			out = s.funcName + "(" + inner + ")"
		}
	} else {
		out = d.QuoteCol(s.colRef)
	}
	if s.over != "" {
		out += " " + s.over
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

// --- 窗口函数（必须配 .Over(...) 才合法）---
// 这些函数无普通列形态，返回 SelectItem（isWindowFunc=true），
// 调用方必须链 .Over(...) 再 .As(...)。

// RowNumber 生成 ROW_NUMBER() 窗口函数。须配 .Over(nil, []string{"col DESC"})。
func RowNumber() SelectItem {
	return SelectItem{isAgg: true, funcName: "ROW_NUMBER", colRef: ""}.windowNoArg()
}

// Rank 生成 RANK()。
func Rank() SelectItem {
	return SelectItem{isAgg: true, funcName: "RANK", colRef: ""}.windowNoArg()
}

// DenseRank 生成 DENSE_RANK()。
func DenseRank() SelectItem {
	return SelectItem{isAgg: true, funcName: "DENSE_RANK", colRef: ""}.windowNoArg()
}

// windowNoArg 把无参窗口函数的渲染标记成 funcName()（无内部列）。
// 复用 RenderSelect 的聚合路径：colRef="" 时 inner 不渲染，得到 funcName()。
func (s SelectItem) windowNoArg() SelectItem {
	s.colRef = "" // 空列引用 → 函数体内为空 → ROW_NUMBER()
	return s
}

// Lag 生成 LAG(col) 窗口函数（向前取 1 行的值）。须配 .Over(...)。
// 需要非 1 的 offset 或默认值时用 fusion.Raw 兜底。
func Lag[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "LAG", colRef: c.ref()}
}

// Lead 生成 LEAD(col)（向后取 1 行）。须配 .Over(...)。
func Lead[T any](c Col[T]) SelectItem {
	return SelectItem{isAgg: true, funcName: "LEAD", colRef: c.ref()}
}
