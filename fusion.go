// Package fusion 是泛型 ORM 的顶层入口。
//
// 提供模型注册（Register）、查询入口（From）、原始 SQL 兜底（Raw）。
// 详见 docs/DESIGN.md。
package fusion

import (
	"context"
	"database/sql"
	"fmt"

	"fusion/dialect"
	"fusion/hook"
	"fusion/meta"
	"fusion/query"
	"fusion/scan"
	"fusion/tx"
)

// 默认方言。可由 SetDefaultDialect 修改。
var defaultDialect dialect.Dialect = dialect.PostgresDialect

// SetDefaultDialect 设置全局默认方言。
func SetDefaultDialect(d dialect.Dialect) { defaultDialect = d }

// DefaultDialect 返回当前默认方言。
func DefaultDialect() dialect.Dialect { return defaultDialect }

// DB 是执行查询所需的最小接口（*sql.DB / *sql.Tx 满足）。
type DB = query.QueryExecer

// Register 注册模型并返回 Table[T]。name 为表名（空则用类型名蛇形化）。
// 默认用 DefaultDialect。
func Register[T any](name string) *meta.Table[T] {
	return meta.Register[T](name)
}

// From 返回绑定到 table、通过 db 执行的 Query[T]，使用默认方言。
func From[T any](t *meta.Table[T], db DB) *query.Query[T] {
	return query.New[T](t, defaultDialect, db)
}

// FromDialect 同 From，但指定方言。
func FromDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect) *query.Query[T] {
	return query.New[T](t, d, db)
}

// Insert 返回绑定到 target 实体的 Inserter（插入 target 中已 Set 的字段）。
func Insert[T any](t *meta.Table[T], db DB, target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, defaultDialect, db, target)
}

// InsertDialect 同 Insert，但指定方言。
func InsertDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect, target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, d, db, target)
}

// Update 返回绑定到 target 实体的 Updater（仅更新 set==true 的字段，见 #3）。
func Update[T any](t *meta.Table[T], db DB, target *T) *query.Updater[T] {
	return query.NewUpdate[T](t, defaultDialect, db, target)
}

// UpdateDialect 同 Update，但指定方言。
func UpdateDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect, target *T) *query.Updater[T] {
	return query.NewUpdate[T](t, d, db, target)
}

// Delete 返回 Deleter。
func Delete[T any](t *meta.Table[T], db DB) *query.Deleter[T] {
	return query.NewDelete[T](t, defaultDialect, db)
}

// DeleteDialect 同 Delete，但指定方言。
func DeleteDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect) *query.Deleter[T] {
	return query.NewDelete[T](t, d, db)
}

// Raw 执行原始 SQL，扫描进 *[]T。out 必须指向已注册模型类型的切片。
// 这是兜底机制（见设计目标：最终允许 raw 语句）。
func Raw[T any](out *[]T, ctx context.Context, db DB, sqlStr string, args ...any) error {
	t := meta.Register[T]("")
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("fusion: raw query: %w", err)
	}
	defer rows.Close()
	res, err := scan.All[T](rows, t.Meta)
	if err != nil {
		return err
	}
	*out = res
	return nil
}

// --- 事务（见 docs/DESIGN.md #6）---

// TxMode 事务嵌套模式（savepoint / reuse）。
type TxMode = tx.Mode

const (
	// TxModeSavepoint 嵌套用 SAVEPOINT，支持部分回滚。
	TxModeSavepoint = tx.TxModeSavepoint
	// TxModeReuse 嵌套复用外层事务。
	TxModeReuse = tx.TxModeReuse
)

// SetDefaultTxMode 设置全局默认事务模式。
func SetDefaultTxMode(m TxMode) { tx.SetDefaultMode(m) }

// Tx 在事务中执行 fn。fn 返回 nil 提交，返回 error 回滚。
// 嵌套时按默认模式（或传入 mode）处理。db 应为 *sql.DB。
// fn 中的 From/Insert/Update/Delete 若用 WrapDB 包装的 DB + 同一 ctx，自动走事务。
func Tx(ctx context.Context, db *sql.DB, fn func(ctx context.Context) error) error {
	return tx.Tx(ctx, db, tx.DefaultMode(), fn)
}

// TxWithMode 同 Tx，但指定本次事务的模式（覆盖默认）。
func TxWithMode(ctx context.Context, db *sql.DB, mode TxMode, fn func(ctx context.Context) error) error {
	return tx.Tx(ctx, db, mode, fn)
}

// --- 钩子（见 docs/DESIGN.md 钩子部分）---

// HookEvent 钩子事件类型。
type HookEvent = hook.Event

const (
	BeforeCreate = hook.BeforeCreate
	AfterCreate  = hook.AfterCreate
	BeforeUpdate = hook.BeforeUpdate
	AfterUpdate  = hook.AfterUpdate
	BeforeDelete = hook.BeforeDelete
	AfterDelete  = hook.AfterDelete
)

// HookFunc 钩子回调类型。
type HookFunc = hook.Func

// OnHook 为模型注册钩子，返回注销函数。modelPtr 应为指向模型的指针（如 (*User)(nil)）。
func OnHook(modelPtr any, event HookEvent, fn HookFunc) (unregister func()) {
	return hook.Register(modelPtr, event, fn)
}

