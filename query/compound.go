package query

import (
	"context"
	"fmt"
	"time"

	"fusion/builder"
	"fusion/dialect"
	"fusion/logging"
	"fusion/meta"
	"fusion/scan"
)

// Compound 是集合复合查询（UNION/INTERSECT/EXCEPT）的可执行构建器。
// 由 fusion 层的 Union/UnionAll/Intersect/Except 构造。
//
// 所有 arm 必须扫描进同一类型 T（列结构一致）。ORDER/LIMIT/OFFSET 作用于整体。
type Compound[T any] struct {
	arms   []compoundArm
	ops    []builder.CompoundOp
	d      dialect.Dialect
	execer queryExecer
	orders []builder.OrderItem
	limit  int
	offset int
}

// compoundArm 一条 SELECT 臂：meta + SelectQuery。
type compoundArm struct {
	meta  *meta.ModelMeta
	query builder.SelectQuery
}

// All 执行复合查询，扫描进 []T（用首个 arm 的 meta 做列路由）。
func (c *Compound[T]) All(ctx context.Context) ([]T, error) {
	if len(c.arms) == 0 {
		return nil, fmt.Errorf("fusion: compound query has no arms")
	}
	cq := builder.CompoundQuery{
		Arms:   c.toBuilderArms(),
		Ops:    c.ops,
		Orders: c.orders,
		Limit:  c.limit,
		Offset: c.offset,
	}
	sqlStr, args := builder.BuildCompound(cq, c.d)
	start := time.Now()
	rows, err := c.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return nil, fmt.Errorf("fusion: compound query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	result, scanErr := scan.All[T](rows, c.arms[0].meta)
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: int64(len(result)), Err: scanErr})
	if scanErr != nil {
		return result, scanErr
	}
	return result, nil
}

// toBuilderArms 转成 builder.CompoundArm 切片。
func (c *Compound[T]) toBuilderArms() []builder.CompoundArm {
	out := make([]builder.CompoundArm, 0, len(c.arms))
	for _, a := range c.arms {
		out = append(out, builder.CompoundArm{Meta: a.meta, Query: a.query})
	}
	return out
}

// OrderBy 设置整体 ORDER BY（作用于复合结果，不是单条 arm）。
func (c *Compound[T]) OrderBy(orders ...builder.OrderItem) *Compound[T] {
	c.orders = append(c.orders, orders...)
	return c
}

// Limit 设置整体 LIMIT。
func (c *Compound[T]) Limit(n int) *Compound[T] { c.limit = n; return c }

// Offset 设置整体 OFFSET。
func (c *Compound[T]) Offset(n int) *Compound[T] { c.offset = n; return c }

// newCompound 构造复合查询构建器（内部用，fusion 层包装）。
// arms/ops 已就绪；d/execer 来自首个 arm 的 Query。
func newCompound[T any](arms []compoundArm, ops []builder.CompoundOp, d dialect.Dialect, execer queryExecer) *Compound[T] {
	return &Compound[T]{arms: arms, ops: ops, d: d, execer: execer}
}

// armFromQuery 从一个 *Query[T] 提取 compoundArm（meta + SelectQuery）。
func armFromQuery[T any](q *Query[T]) compoundArm {
	return compoundArm{meta: q.table.Meta, query: q.buildSelectQuery()}
}

// Union 构造 UNION 复合查询（去重）。至少两个 arm。
// 后续可链式 .OrderBy/.Limit/.Offset/.All。
func Union[T any](first *Query[T], others ...*Query[T]) *Compound[T] {
	return combine(builder.OpUnion, first, others)
}

// UnionAll 构造 UNION ALL（不去重）。
func UnionAll[T any](first *Query[T], others ...*Query[T]) *Compound[T] {
	return combine(builder.OpUnionAll, first, others)
}

// Intersect 构造 INTERSECT。
func Intersect[T any](first *Query[T], others ...*Query[T]) *Compound[T] {
	return combine(builder.OpIntersect, first, others)
}

// Except 构造 EXCEPT。
func Except[T any](first *Query[T], others ...*Query[T]) *Compound[T] {
	return combine(builder.OpExcept, first, others)
}

// combine 构造复合查询：first 与 others 之间用 op 连接。
func combine[T any](op builder.CompoundOp, first *Query[T], others []*Query[T]) *Compound[T] {
	arms := []compoundArm{armFromQuery(first)}
	ops := make([]builder.CompoundOp, 0, len(others))
	for range others {
		ops = append(ops, op)
	}
	for _, o := range others {
		arms = append(arms, armFromQuery(o))
	}
	return newCompound[T](arms, ops, first.d, first.execer)
}
