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
	"errors"
	"fmt"
	"reflect"
	"time"

	"fusion/builder"
	"fusion/col"
	"fusion/dialect"
	"fusion/expr"
	"fusion/logging"
	"fusion/meta"
	"fusion/relation"
	"fusion/scan"
)

// ErrNotFound 表示查询无结果。根包 fusion.ErrNotFound 是此别名。
// errors.Is(err, ErrNotFound) 与 errors.Is(err, sql.ErrNoRows) 均兼容。
var ErrNotFound = errors.New("fusion: not found")

// Query 是 SELECT 查询构建器。
type Query[T any] struct {
	table     *meta.Table[T]
	d         dialect.Dialect
	execer    queryExecer

	where     expr.Expr
	orders    []builder.OrderItem
	limit     int
	offset    int
	preloads  []string // 要预加载的关联字段名

	selectCols []builder.SelectItem // 投影列（空则整表）
	joins      []builder.JoinSpec   // JOIN 子句
	groupBy    []builder.GroupItem  // GROUP BY
	having     expr.Expr            // HAVING
	distinct   bool
	alias      string               // 主表别名（Join/投影场景需要）
	lockClause string               // 锁子句（FOR UPDATE 等）
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

// Preload 添加要预加载的关联字段名（eager loading，IN 批量策略避免 N+1）。
// 可多次调用加载多个关联，或用嵌套路径（"Posts"）。
// 不调用则不查关联（默认行为，见文档决策 7）。
func (q *Query[T]) Preload(fieldNames ...string) *Query[T] {
	q.preloads = append(q.preloads, fieldNames...)
	return q
}

// Select 设置投影列（灵活 Join / 聚合查询用）。配合 AllInto 扫描进投影结构体。
func (q *Query[T]) Select(items ...builder.SelectItem) *Query[T] {
	q.selectCols = append(q.selectCols, items...)
	return q
}

// Join 添加 JOIN 子句。kind 为 "INNER"/"LEFT"/"RIGHT"/"FULL"，
// joinedTable 为已注册的 Table（TableOf 接口，如 Depts），alias 为连接表别名，
// on 为 EqCol 组成的 ON 条件。
//
// 并发安全：列引用用稳定表名（注册时确定），别名在 render 时由 builder 映射替换。
// on 可在任意时刻构造（无需先 As），因为 ref() 始终返回 "表名.列名"。
//
// 用法：
//   fusion.From(Users, db).As("u").
//       Join(fusion.InnerJoin, Depts, "d", Users.Proto.DeptID.EqCol(Depts.Proto.ID))
func (q *Query[T]) Join(kind string, joinedTable meta.TableOf, alias string, on expr.Expr) *Query[T] {
	q.joins = append(q.joins, builder.JoinSpec{
		Kind:  kind,
		Table: joinedTable.ModelMeta().Table,
		Alias: alias,
		On:    on,
	})
	return q
}

// GroupBy 添加 GROUP BY 列引用。
func (q *Query[T]) GroupBy(items ...builder.GroupItem) *Query[T] {
	q.groupBy = append(q.groupBy, items...)
	return q
}

// Having 设置 HAVING 条件（聚合后的过滤）。
func (q *Query[T]) Having(e expr.Expr) *Query[T] {
	q.having = e
	return q
}

// Distinct 设置去重。
func (q *Query[T]) Distinct() *Query[T] {
	q.distinct = true
	return q
}

// ForUpdate 加 FOR UPDATE 锁子句（事务内悲观锁）。
func (q *Query[T]) ForUpdate() *Query[T] {
	q.lockClause = "FOR UPDATE"
	return q
}

// ForShare 加 FOR SHARE 锁子句。
func (q *Query[T]) ForShare() *Query[T] {
	q.lockClause = "FOR SHARE"
	return q
}

// ForUpdateNoWait 加 FOR UPDATE NOWAIT（锁失败立即报错）。
func (q *Query[T]) ForUpdateNoWait() *Query[T] {
	q.lockClause = "FOR UPDATE NOWAIT"
	return q
}

// As 设置主表别名（仅存到 Query 实例，render 时由 builder 用 表名→别名 映射替换）。
// 不修改全局 Proto，并发安全。Join/聚合场景需要 As 才能在 SQL 输出表别名。
func (q *Query[T]) As(alias string) *Query[T] {
	q.alias = alias
	return q
}

// buildSelectQuery 把 Query 的字段组装成 builder.SelectQuery。
func (q *Query[T]) buildSelectQuery() builder.SelectQuery {
	return builder.SelectQuery{
		Alias:      q.alias,
		SelectCols: q.selectCols,
		Joins:      q.joins,
		Where:      q.where,
		GroupBy:    q.groupBy,
		Having:     q.having,
		Orders:     q.orders,
		Distinct:   q.distinct,
		Limit:      q.limit,
		Offset:     q.offset,
		LockClause: q.lockClause,
	}
}


// AllInto 执行查询，扫描进 out 指向的 []V 切片（投影结构体，需已 Register）。
// 用于灵活 Join / 聚合查询的结果承载（V 是自定义投影结构体，非模型 T）。
func (q *Query[T]) AllInto(ctx context.Context, out any) error {
	sq := q.buildSelectQuery()
	sqlStr, args := builder.BuildSELECT(q.table.Meta, sq, q.d)
	start := time.Now()
	rows, err := q.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return fmt.Errorf("fusion: query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	// out 是 *[]V。反射取出 V 的类型，找其 ModelMeta（需已 Register），用 scan.All 扫描。
	rv := reflect.ValueOf(out)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("fusion: AllInto requires *[]V, got %T", out)
	}
	elemType := rv.Type().Elem()
	tab := meta.Lookup(elemType)
	if tab == nil {
		return fmt.Errorf("fusion: AllInto: projection type %s not registered (use fusion.Register first)", elemType)
	}
	result, scanErr := scan.AllRaw(rows, tab.ModelMeta(), elemType)
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: int64(reflect.ValueOf(result).Len()), Err: scanErr})
	if scanErr != nil {
		return scanErr
	}
	rv.Set(reflect.ValueOf(result))
	return nil
}

// All 执行查询，扫描进 []T。
func (q *Query[T]) All(ctx context.Context) ([]T, error) {
	sq := q.buildSelectQuery()
	sqlStr, args := builder.BuildSELECT(q.table.Meta, sq, q.d)

	start := time.Now()
	rows, err := q.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return nil, fmt.Errorf("fusion: query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	result, scanErr := scan.All[T](rows, q.table.Meta)
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: int64(len(result)), Err: scanErr})
	if scanErr != nil {
		return result, scanErr
	}
	// 显式 Close rows，释放连接（单连接模式下 Preload 子查询需要复用连接）
	rows.Close()
	// Preload 回填（IN 批量，避免 N+1）
	if len(q.preloads) > 0 {
		if err := q.runPreloads(ctx, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// runPreloads 对 result 执行所有已配置的 Preload。
func (q *Query[T]) runPreloads(ctx context.Context, result []T) error {
	for _, field := range q.preloads {
		rm := relation.Lookup(q.table.Meta.Type, field)
		if rm == nil {
			return fmt.Errorf("fusion: Preload: relation %q not registered on %s", field, q.table.Meta.Type)
		}
		// 传切片指针使元素可寻址（Preload 内部反射回填字段）
		if err := relation.Preload(ctx, q.execer, q.d, &result, rm); err != nil {
			return fmt.Errorf("fusion: Preload %q: %w", field, err)
		}
	}
	return nil
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

	start := time.Now()
	rows, err := q.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return zero, fmt.Errorf("fusion: query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	result, scanErr := scan.One[T](rows, q.table.Meta)
	rowsN := int64(1)
	if scanErr != nil {
		rowsN = 0
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsN, Err: scanErr})
	if scanErr != nil {
		// sql.ErrNoRows 包装为 ErrNotFound（errors.Is 兼容两者）
		if errors.Is(scanErr, sql.ErrNoRows) {
			return result, fmt.Errorf("%w: %w", ErrNotFound, sql.ErrNoRows)
		}
		return result, scanErr
	}
	// 显式 Close rows，释放连接（单连接模式下 Preload 子查询需要复用连接）
	rows.Close()
	// Preload 回填（单实体包成切片）
	if len(q.preloads) > 0 {
		single := []T{result}
		if err := q.runPreloads(ctx, single); err != nil {
			return result, err
		}
		result = single[0]
	}
	return result, nil
}

// SubquerySQL 生成子查询 SQL（不含外层括号）+ 参数，供 EXISTS/IN 子查询用。
// 实现 expr.SubqueryProvider。占位符由外层 render 时重写（参数并入外层）。
func (q *Query[T]) SubquerySQL() (string, []any) {
	sq := q.buildSelectQuery()
	return builder.BuildSubquerySQL(q.table.Meta, sq, q.d)
}

// Count 执行 SELECT COUNT(*) 查询，返回匹配 WHERE 的行数。
// 用 builder.renderer 复用占位符/QuoteCol 逻辑（消除重复的 countRenderer）。
func (q *Query[T]) Count(ctx context.Context) (int64, error) {
	// 用 builder 的 renderer 渲染 WHERE（复用占位符与列引用）
	sq := builder.SelectQuery{
		SelectCols: []builder.SelectItem{col.Count[int64]()},
		Where:      q.where,
	}
	// Count 不需要 ORDER BY/LIMIT，直接用 BuildSELECT 但只取 COUNT(*) 投影
	sqlStr, args := builder.BuildSELECT(q.table.Meta, sq, q.d)
	// BuildSELECT 会生成 SELECT COUNT(*) FROM ... WHERE ...，但 COUNT(*) 无 AS 别名，
	// 扫描时按列位置取（第 1 列）。这里直接 QueryRowContext + Scan int64。
	var n int64
	start := time.Now()
	row := q.execer.QueryRowContext(ctx, sqlStr, args...)
	if err := row.Scan(&n); err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return 0, fmt.Errorf("fusion: count: %w (sql=%s)", err, sqlStr)
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: 1})
	return n, nil
}

// 编译期断言：col.Order 实现 builder.OrderItem
var _ builder.OrderItem = col.Order{}
