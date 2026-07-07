// Package col 实现字段描述符 Col[T]。
//
// Col[T] 是模型字段类型，同时承担数据容器与查询描述符职责：
//   - 携带列名/表别名等元数据（Register 反射填充）
//   - 提供类型安全的比较方法（Eq/Gt/... → expr.Expr）
//   - 提供读写访问（Get/Set）与透明序列化（JSON/SQL）
//
// 详见 docs/DESIGN.md 决策 1、3。
package col

import (
	"database/sql/driver"

	"fusion/expr"
	"fusion/meta"
)

// Col 是泛型字段描述符。
//
// 零值的 Col 可用于模型定义；Register 后字段携带列名等元数据。
// Col[T] 的 T 若为指针类型（如 *string），则 nil 表示数据库 NULL（见 #3）。
type Col[T any] struct {
	val   T
	set   bool // 是否被显式 Set 过（用于 UPDATE 局部更新，见 #3）
	col   string // 数据库列名（Register 填充）
	table string // 表别名（builder 渲染时设置）
}

// --- meta.FieldDescriptor 实现 ---

// SetMeta 由 meta.Register 反射调用，填充列名与表名。
// table 存储稳定的表名（注册时确定，不可变），别名在 render 时由 builder 映射替换。
func (c *Col[T]) SetMeta(m meta.FieldMeta) {
	c.col = m.Column
	c.table = m.Table
}

// TableName 返回字段所属表名（注册时填充，稳定不变）。
func (c *Col[T]) TableName() string { return c.table }

// --- 读写访问（见 #3） ---

// Get 返回字段值。若 T 是指针类型且为 nil，表示数据库 NULL。
func (c *Col[T]) Get() T { return c.val }

// Set 设置字段值并标记为"已设置"，用于 UPDATE 局部更新。
func (c *Col[T]) Set(v T) {
	c.val = v
	c.set = true
}

// IsSet 报告字段是否被显式 Set 过。
func (c *Col[T]) IsSet() bool { return c.set }

// Reset 清除字段的 set 标志（保留当前值），让 IsSet() 回到 false。
// 用于复用对象时重置脏状态（例如把同一个 Col 切换到"全量保存"前的基线）。
// 注意：Scan 不置 set 标志（见文档"陷阱1"），加载后的对象默认全部未 dirty；
// Reset 主要服务于"显式 Set 后又想撤销 dirty"的场景，配合 Update().AllFields() 使用。
func (c *Col[T]) Reset() { c.set = false }

// IsZero 报告字段是否处于"零状态"——即从未被 Set 过。
// 注意：Set(0) 之后 IsZero 返回 false（用户已明确赋值，即使值是零值）。
// 这与 #3 的 set 标志语义一致：是否赋过值，而非值是否为零。
func (c *Col[T]) IsZero() bool {
	return !c.set
}

// FieldValuer 是所有 Col[T]/Rel[T] 字段类型实现的统一接口，
// 供 meta 反射收集字段信息（列名、是否赋值、SQL 值），用于 DML 生成。
type FieldValuer interface {
	ColName() string
	IsSet() bool
	SQLValue() (any, error)
}

// SQLValue 返回字段的 SQL 值（解指针、NULL 转换），供 DML 生成参数使用。
func (c *Col[T]) SQLValue() (any, error) {
	v, err := c.Value()
	return v, err
}

// --- 比较方法（返回 expr.Expr，见 #1、#2） ---

// ColName 返回列名（不含表别名），供 builder 内部使用。
func (c *Col[T]) ColName() string { return c.col }

// ref 返回表别名.列名的原始形式（未 quote，render 时由 Renderer quote）。
func (c Col[T]) ref() string {
	if c.table != "" {
		return c.table + "." + c.col
	}
	return c.col
}

// eqExpr 生成比较表达式。
func (c Col[T]) compareExpr(op string, v T) expr.Expr {
	return expr.LeafParam(c.ref(), op, v)
}

// Eq 生成等于表达式（=）。
func (c Col[T]) Eq(v T) expr.Expr { return c.compareExpr("=", v) }

// Ne 生成不等于表达式（<>）。
func (c Col[T]) Ne(v T) expr.Expr { return c.compareExpr("<>", v) }

// Gt 生成大于表达式（>）。
func (c Col[T]) Gt(v T) expr.Expr { return c.compareExpr(">", v) }

// Gte 生成大于等于表达式（>=）。
func (c Col[T]) Gte(v T) expr.Expr { return c.compareExpr(">=", v) }

// Lt 生成小于表达式（<）。
func (c Col[T]) Lt(v T) expr.Expr { return c.compareExpr("<", v) }

// Lte 生成小于等于表达式（<=）。
func (c Col[T]) Lte(v T) expr.Expr { return c.compareExpr("<=", v) }

// Like 生成模糊匹配表达式（LIKE）。
func (c Col[T]) Like(v T) expr.Expr { return c.compareExpr("LIKE", v) }

// NotLike 生成不匹配表达式（NOT LIKE）。
func (c Col[T]) NotLike(v T) expr.Expr { return c.compareExpr("NOT LIKE", v) }

// EqDistinct 生成 NULL 安全等于。
// 与 Eq 不同：当值为 NULL 时，Eq 生成 "col = NULL"（永假），EqDistinct 正确匹配 NULL。
// 方言感知：PG/SQLite 用 "IS NOT DISTINCT FROM"；MySQL 用 "<=>"。
func (c Col[T]) EqDistinct(v T) expr.Expr { return expr.LeafDistinct(c.ref(), v, false) }

// NeDistinct 生成 NULL 安全不等（IS DISTINCT FROM / NOT <=>）。
func (c Col[T]) NeDistinct(v T) expr.Expr { return expr.LeafDistinct(c.ref(), v, true) }

// In 生成 IN 表达式。
func (c Col[T]) In(vs []T) expr.Expr {
	args := make([]any, len(vs))
	for i, v := range vs {
		args[i] = v
	}
	return expr.LeafMulti(c.ref(), "IN", args)
}

// NotIn 生成 NOT IN 表达式。
func (c Col[T]) NotIn(vs []T) expr.Expr {
	args := make([]any, len(vs))
	for i, v := range vs {
		args[i] = v
	}
	return expr.LeafMulti(c.ref(), "NOT IN", args)
}

// Between 生成区间表达式（col BETWEEN lo AND hi）。
func (c Col[T]) Between(lo, hi T) expr.Expr {
	return expr.LeafBetween(c.ref(), lo, hi, false)
}

// NotBetween 生成 NOT BETWEEN 表达式。
func (c Col[T]) NotBetween(lo, hi T) expr.Expr {
	return expr.LeafBetween(c.ref(), lo, hi, true)
}

// IsNull 生成 IS NULL 表达式。
func (c Col[T]) IsNull() expr.Expr {
	return expr.LeafRaw(c.ref(), "IS NULL")
}

// IsNotNull 生成 IS NOT NULL 表达式。
func (c Col[T]) IsNotNull() expr.Expr {
	return expr.LeafRaw(c.ref(), "IS NOT NULL")
}

// EqCol 生成列对列比较（用于 JOIN ON），无参数。
func (c Col[T]) EqCol(other Col[T]) expr.Expr {
	return expr.LeafColCol(c.ref(), "=", other.ref())
}

// --- Order（排序方向） ---

// Order 表示排序子句。
type Order struct {
	col string
	dir string
}

// Asc 升序。
func (c Col[T]) Asc() Order { return Order{col: c.ref(), dir: "ASC"} }

// Desc 降序。
func (c Col[T]) Desc() Order { return Order{col: c.ref(), dir: "DESC"} }

// RenderClause 渲染排序子句，d 提供列引用。
func (o Order) RenderClause(d expr.Renderer) string { return d.QuoteCol(o.col) + " " + o.dir }

// GroupCol 是 GROUP BY 项（纯列引用，无方向）。
type GroupCol struct{ ref string }

// GroupBy 生成 GROUP BY 列引用。
func (c Col[T]) GroupBy() GroupCol { return GroupCol{ref: c.ref()} }

// RenderClause 渲染 GROUP BY 列引用（实现 builder.GroupItem）。
func (g GroupCol) RenderClause(d expr.Renderer) string { return d.QuoteCol(g.ref) }

// --- 透明序列化（见决策1：JSON/SQL 全自动透明） ---

// Valuer 用于把 Col[T] 内的值（可能是指针）转换为 SQL 可接受的值。
func derefAny(v any) any {
	// 若 T 是指针，driver 需要解引用；nil 指针 → nil（NULL）。
	switch x := v.(type) {
	case nil:
		return nil
	case driver.Valuer:
		val, err := x.Value()
		if err == nil {
			return val
		}
		return nil
	}
	// 通过反射解指针（T 可能是 *string 等）
	return maybeDeref(v)
}
