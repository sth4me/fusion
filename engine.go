package fusion

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/logging"
	"github.com/sth4me/fusion/meta"
	"github.com/sth4me/fusion/query"
	"github.com/sth4me/fusion/tx"
)

// Engine 承载连接相关状态（数据库、方言、可选 logger/慢阈值/事务默认选项）。
//
// 设计：全局 API（fusion.From/SetDefaultDialect/...）委托给隐式的 defaultEngine，
// 多库场景用 New 显式创建独立 Engine，互不干扰。模型注册（meta/relation/hook）
// 仍是进程级全局，与 Engine 无关。
type Engine struct {
	db      *sql.DB
	dialect dialect.Dialect
	logger  *slog.Logger // nil = 用全局 logging logger
	slow    time.Duration // <0 = 用全局慢阈值；0 不影响（用全局）
	txMode  tx.Mode
	txOpts  *tx.Options // 默认事务选项（隔离级别/重试），nil = 默认
}

// Option 配置 Engine（函数式选项）。
type Option func(*Engine)

// WithDialect 设置 Engine 的方言。
func WithDialect(d dialect.Dialect) Option {
	return func(e *Engine) { e.dialect = d }
}

// WithLogger 设置 Engine 专属 logger（覆盖全局，多库可各自记不同 logger）。
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) { e.logger = l }
}

// WithSlowThreshold 设置 Engine 专属慢查询阈值（覆盖全局）。
func WithSlowThreshold(d time.Duration) Option {
	return func(e *Engine) { e.slow = d }
}

// WithTxMode 设置 Engine 的默认事务嵌套模式（savepoint/reuse）。
func WithTxMode(m tx.Mode) Option {
	return func(e *Engine) { e.txMode = m }
}

// WithTxIsolation 设置 Engine 事务的默认隔离级别。
func WithTxIsolation(level sql.IsolationLevel) Option {
	return func(e *Engine) {
		if e.txOpts == nil {
			e.txOpts = &tx.Options{}
		}
		if e.txOpts.TxOptions == nil {
			e.txOpts.TxOptions = &sql.TxOptions{}
		}
		e.txOpts.TxOptions.Isolation = level
	}
}

// WithTxReadOnly 设置 Engine 事务默认只读。
func WithTxReadOnly() Option {
	return func(e *Engine) {
		if e.txOpts == nil {
			e.txOpts = &tx.Options{}
		}
		if e.txOpts.TxOptions == nil {
			e.txOpts.TxOptions = &sql.TxOptions{}
		}
		e.txOpts.TxOptions.ReadOnly = true
	}
}

// WithTxRetry 设置 Engine 事务的死锁重试默认配置。
func WithTxRetry(max int, base, maxDelay time.Duration) Option {
	return func(e *Engine) {
		if e.txOpts == nil {
			e.txOpts = &tx.Options{}
		}
		e.txOpts.RetryDeadlocks = max
		e.txOpts.RetryBaseDelay = base
		e.txOpts.RetryMaxDelay = maxDelay
	}
}

// New 创建独立 Engine。opts 至少应包含 WithDialect（否则用包级默认 PostgresDialect）。
// db 为底层 *sql.DB。
func New(db *sql.DB, opts ...Option) *Engine {
	e := &Engine{
		db:      db,
		dialect: DefaultDialect(), // 加锁读取（避免与 SetDefaultDialect/Open 的 data race）
		txMode:  0,                // 0 = 用 tx.DefaultMode()
		slow:    -1,               // -1 = 用全局慢阈值
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// SetDialect 修改 Engine 的方言。
func (e *Engine) SetDialect(d dialect.Dialect) { e.dialect = d }

// Dialect 返回 Engine 当前方言。
func (e *Engine) Dialect() dialect.Dialect { return e.dialect }

// DB 返回 Engine 的底层 *sql.DB（供 Close/Ping）。
func (e *Engine) DB() *sql.DB { return e.db }

// wrapCtx 把 Engine 的 logger/slow 覆盖挂到 ctx，供 logging.LogQuery 读取。
func (e *Engine) wrapCtx(ctx context.Context) context.Context {
	if e.logger == nil && e.slow < 0 {
		return ctx // 无覆盖，沿用全局
	}
	return logging.WithOverride(ctx, e.logger, e.slow)
}

// Ctx 返回挂了 Engine 的 logger/slow 覆盖的 ctx。
// 供显式查询时传递：fusion.EFrom(engine, Users).Where(...).All(engine.Ctx(ctx))。
// 这样查询的日志走 Engine 的 logger 而非全局（多库各记不同 logger 的关键）。
// Engine.Tx 内部已自动包装 ctx，事务回调里无需再调 Ctx。
func (e *Engine) Ctx(ctx context.Context) context.Context {
	return e.wrapCtx(ctx)
}

// wrapped 返回绑定 Engine 的 DB（ctx 感知）。
func (e *Engine) wrapped() DB { return WrapDB(e.db) }

// EFrom 返回绑定到 table、通过 Engine 执行的 Query[T]。
// （Go 1.26 不支持泛型方法，故 Engine 的查询入口为顶层函数，前缀 E 避免与全局 From 冲突。）
// 用法：fusion.EFrom(engine, Users).Where(...).All(ctx)
func EFrom[T any](e *Engine, t *meta.Table[T]) *query.Query[T] {
	return query.New[T](t, e.dialect, e.wrapped())
}

// EInsert 返回绑定到 target 实体的 Inserter（经 Engine）。
func EInsert[T any](e *Engine, t *meta.Table[T], target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, e.dialect, e.wrapped(), target)
}

// EInsertBatch 批量插入（经 Engine）。
func EInsertBatch[T any](e *Engine, t *meta.Table[T], targets []*T) *query.Inserter[T] {
	return query.NewInsertBatch[T](t, e.dialect, e.wrapped(), targets)
}

// EUpsert 同 EInsert，语义提示用 OnConflict。
func EUpsert[T any](e *Engine, t *meta.Table[T], target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, e.dialect, e.wrapped(), target)
}

// EUpdate 返回绑定到 target 实体的 Updater（经 Engine）。
func EUpdate[T any](e *Engine, t *meta.Table[T], target *T) *query.Updater[T] {
	return query.NewUpdate[T](t, e.dialect, e.wrapped(), target)
}

// EDelete 返回 Deleter（经 Engine）。
func EDelete[T any](e *Engine, t *meta.Table[T]) *query.Deleter[T] {
	return query.NewDelete[T](t, e.dialect, e.wrapped())
}

// EDeleteByID 按单列主键删除（id 绑定到首个 PK 列）。复合主键用 EDeleteByIDs。
func EDeleteByID[T any](e *Engine, t *meta.Table[T], id any) *query.Deleter[T] {
	pkCols := t.Meta.PrimaryKeyColumns()
	if len(pkCols) == 0 {
		return query.NewDeleteByID[T](t, e.dialect, e.wrapped(), nil)
	}
	return query.NewDeleteByID[T](t, e.dialect, e.wrapped(), map[string]any{pkCols[0]: id})
}

// EDeleteByIDs 按主键删除（支持复合主键，ids 为列名→值）。
func EDeleteByIDs[T any](e *Engine, t *meta.Table[T], ids map[string]any) *query.Deleter[T] {
	return query.NewDeleteByID[T](t, e.dialect, e.wrapped(), ids)
}

// ERaw 执行原始 SQL，扫描进 *[]T（经 Engine，含 Engine 的 logger 覆盖）。
func ERaw[T any](e *Engine, out *[]T, ctx context.Context, sqlStr string, args ...any) error {
	return Raw[T](out, e.wrapCtx(ctx), e.wrapped(), sqlStr, args...)
}

// EExec 执行原始写 SQL（经 Engine，事务感知 + logger 覆盖）。
// 用于 OnConflict 不支持的累加语义、批量 UPDATE FROM 等。
// 返回 sql.Result（可取 RowsAffected）。
func EExec(e *Engine, ctx context.Context, sqlStr string, args ...any) (sql.Result, error) {
	return Exec(e.wrapCtx(ctx), e.wrapped(), sqlStr, args...)
}

// EExecReturning 执行原始写 SQL 并扫描 RETURNING 子句（经 Engine，事务感知）。
// 适合 INSERT ... RETURNING "id" 之类场景。
func EExecReturning[T any](e *Engine, out *[]T, ctx context.Context, sqlStr string, args ...any) error {
	return ExecReturning[T](out, e.wrapCtx(ctx), e.wrapped(), sqlStr, args...)
}

// ETx 在事务中执行 fn（用 Engine 的默认事务选项/模式）。
// fn 中用 EFrom(engine, t)/EInsert(engine, ...) 并传 fn 的 ctx，自动走事务。
func ETx(e *Engine, ctx context.Context, fn func(ctx context.Context) error) error {
	mode := e.txMode
	if mode == 0 {
		mode = tx.DefaultMode()
	}
	ctx = e.wrapCtx(ctx)
	return tx.TxWithOpts(ctx, e.db, mode, e.txOpts, func(c context.Context) error {
		return fn(e.wrapCtx(c))
	})
}
