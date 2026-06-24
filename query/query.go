// Package query 提供类型安全的查询构建器 Query[T]。
//
// Query[T] 通过链式调用配置 Where/OrderBy/Limit/Offset，最终 All/One 执行查询
// 并扫描进 []T。字段引用通过 col.Col[T] 的描述符方法（Eq/Gt/Asc 等）实现。
//
// 详见 docs/DESIGN.md 决策 1、2、3。
package query

import (
	"context"
	"database/sql"
	"fmt"

	"fusion/builder"
	"fusion/col"
	"fusion/dialect"
	"fusion/expr"
	"fusion/meta"
	"fusion/scan"
)

// Query 是 SELECT 查询构建器。
type Query[T any] struct {
	table  *meta.Table[T]
	d      dialect.Dialect
	execer queryExecer

	where  expr.Expr
	orders []builder.OrderItem
	limit  int
	offset int
}

// QueryExecer 抽象执行 SQL 的能力（*sql.DB 或 *sql.Tx 都满足）。
type QueryExecer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// queryExecer 是 QueryExecer 的包内别名。
type queryExecer = QueryExecer

// New 构造一个绑定到 table、通过 execer 执行的 Query。
func New[T any](table *meta.Table[T], d dialect.Dialect, execer queryExecer) *Query[T] {
	return &Query[T]{table: table, d: d, execer: execer}
}

// Where 设置 WHERE 条件（仅接受 Expr，见决策2）。多次调用覆盖。
func (q *Query[T]) Where(e expr.Expr) *Query[T] {
	q.where = e
	return q
}

// OrderBy 追加排序子句（col.Order，即 Col.Asc()/Desc() 的返回值）。
func (q *Query[T]) OrderBy(orders ...builder.OrderItem) *Query[T] {
	q.orders = append(q.orders, orders...)
	return q
}

// Limit 设置 LIMIT。
func (q *Query[T]) Limit(n int) *Query[T] {
	q.limit = n
	return q
}

// Offset 设置 OFFSET。
func (q *Query[T]) Offset(n int) *Query[T] {
	q.offset = n
	return q
}

// All 执行查询，扫描进 []T。
func (q *Query[T]) All(ctx context.Context) ([]T, error) {
	sqlStr, args := builder.BuildSELECT(q.table.Meta, builder.SelectQuery{
		Where:  q.where,
		Orders: q.orders,
		Limit:  q.limit,
		Offset: q.offset,
	}, q.d)

	rows, err := q.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("fusion: query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	return scan.All[T](rows, q.table.Meta)
}

// One 执行查询，返回第一行（自动加 LIMIT 1）。无结果返回 sql.ErrNoRows。
func (q *Query[T]) One(ctx context.Context) (T, error) {
	var zero T
	// 复用 LIMIT 1 但不修改原 query 的 limit（拷贝）
	saved := q.limit
	q.limit = 1
	sqlStr, args := builder.BuildSELECT(q.table.Meta, builder.SelectQuery{
		Where:  q.where,
		Orders: q.orders,
		Limit:  q.limit,
		Offset: q.offset,
	}, q.d)
	q.limit = saved

	rows, err := q.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return zero, fmt.Errorf("fusion: query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	return scan.One[T](rows, q.table.Meta)
}

// Count 执行 SELECT COUNT(*) 查询。
func (q *Query[T]) Count(ctx context.Context) (int64, error) {
	r := &countRenderer{d: q.d}
	// 重新构造：SELECT COUNT(*) FROM ... WHERE ...
	whereSQL := ""
	if !q.where.IsZero() {
		whereSQL = q.where.Render(r)
	}
	sqlStr := "SELECT COUNT(*) FROM " + q.d.QuoteTable(q.table.Meta.Table)
	if whereSQL != "" {
		sqlStr += " WHERE " + whereSQL
	}
	var n int64
	row := q.execer.QueryRowContext(ctx, sqlStr, r.args...)
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("fusion: count: %w (sql=%s)", err, sqlStr)
	}
	return n, nil
}

// countRenderer 仅收集 WHERE 参数（不含列引用业务，复用 expr.Renderer）。
type countRenderer struct {
	d    dialect.Dialect
	phIdx int
	args []any
}

func (c *countRenderer) NextPlaceholder() string { c.phIdx++; return c.d.Placeholder(c.phIdx) }
func (c *countRenderer) AddParam(v any)          { c.args = append(c.args, v) }
func (c *countRenderer) QuoteCol(tc string) string {
	// 委托给一个临时 builder renderer 的引用逻辑
	if i := indexByte(tc, '.'); i >= 0 {
		return c.d.QuoteIdent(tc[:i]) + "." + c.d.QuoteIdent(tc[i+1:])
	}
	return c.d.QuoteIdent(tc)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// 编译期断言：col.Order 实现 builder.OrderItem
var _ builder.OrderItem = col.Order{}
