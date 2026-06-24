package fusion

import (
	"context"
	"database/sql"

	"fusion/tx"
)

// ctxExecer 包装一个基础 DB，执行时若 ctx 中有事务则用事务，否则用基础 DB。
// 这样 From/Insert/Update/Delete 无需显式接收 tx，只要调用方在 orm.Tx 回调里
// 用同一个 ctx，操作自动走事务。
type ctxExecer struct {
	base *sql.DB
}

// newCtxExecer 构造一个 ctx 感知的执行器。
func newCtxExecer(db DB) *ctxExecer {
	if c, ok := db.(*ctxExecer); ok {
		return c
	}
	if real, ok := db.(*sql.DB); ok {
		return &ctxExecer{base: real}
	}
	// 已经是 ctxExecer 或其他实现，原样包装（base 为 nil 时回退到传入的）
	return &ctxExecer{}
}

func (c *ctxExecer) resolve(ctx context.Context) DB {
	if r := tx.FromContext(ctx); r != nil {
		return r // 事务中的 Runner（*sql.Tx）
	}
	return c.base
}

func (c *ctxExecer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.resolve(ctx).QueryContext(ctx, query, args...)
}

func (c *ctxExecer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.resolve(ctx).QueryRowContext(ctx, query, args...)
}

func (c *ctxExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.resolve(ctx).ExecContext(ctx, query, args...)
}

// WrapDB 包装 *sql.DB 为 ctx 感知执行器。
// 之后 From/Insert/Update/Delete 传入的 DB 若是 WrapDB 的结果，
// 则在 orm.Tx 回调中自动走事务。
func WrapDB(db *sql.DB) DB {
	return &ctxExecer{base: db}
}
