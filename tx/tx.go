// Package tx 提供事务管理，支持嵌套（savepoint / 复用两种模式）。
//
// orm.Tx 通过 context 传递事务状态。嵌套时按默认模式处理：
//   - TxModeSavepoint：内层用 SAVEPOINT，支持部分回滚
//   - TxModeReuse：内层复用外层事务，提交/回滚为 no-op
//
// 详见 docs/DESIGN.md #6。
package tx

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Mode 是事务嵌套模式。
type Mode int

const (
	// TxModeSavepoint：嵌套时用 SAVEPOINT，支持部分回滚（推荐默认）。
	TxModeSavepoint Mode = iota
	// TxModeReuse：嵌套时复用外层事务，提交/回滚为 no-op。
	TxModeReuse
)

var (
	defaultModeMu sync.RWMutex
	defaultMode   = TxModeSavepoint
)

// SetDefaultMode 设置全局默认事务模式。
func SetDefaultMode(m Mode) {
	defaultModeMu.Lock()
	defaultMode = m
	defaultModeMu.Unlock()
}

// DefaultMode 返回当前默认事务模式。
func DefaultMode() Mode {
	defaultModeMu.RLock()
	defer defaultModeMu.RUnlock()
	return defaultMode
}

// txKey 是 context 中事务状态的 key 类型（避免冲突）。
type txKey struct{}

// frame 描述当前 context 中的事务帧。
type frame struct {
	db        *sql.DB    // 顶层时为原始 DB，用于 BeginTx
	sqlTx     *sql.Tx    // 当前事务（顶层或复用时指向外层）
	isNested  bool       // 是否嵌套（内层）
	spName    string     // savepoint 名（savepoint 模式时）
	mode      Mode       // 本帧使用的模式
	spMu      sync.Mutex // savepoint 计数器锁（仅顶层帧使用）
	spCounter int        // 顶层帧的 savepoint 计数器（命名唯一性）
	top       *frame     // 指向顶层帧（内层用于访问计数器）
}

// DB 接口：需要 BeginTx 能力的数据库。
type Beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Runner 是事务上下文中执行 SQL 的接口（*sql.DB 或 *sql.Tx 满足）。
type Runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// FromContext 从 ctx 提取当前事务的 Runner（若在事务中）。
// 不在事务中返回 nil。
func FromContext(ctx context.Context) Runner {
	if f, ok := ctx.Value(txKey{}).(*frame); ok && f != nil {
		if f.sqlTx != nil {
			return f.sqlTx
		}
	}
	return nil
}

// Tx 在事务中执行 fn。fn 返回 nil 则提交，返回 error 则回滚。
// mode 为当前模式（嵌套时用）；传 0 用默认模式。
func Tx(ctx context.Context, db Beginner, mode Mode, fn func(ctx context.Context) error) error {
	if mode == 0 {
		mode = DefaultMode()
	}

	// 检测外层事务
	parent, hasParent := ctx.Value(txKey{}).(*frame)
	if hasParent && parent != nil {
		return runNested(ctx, db, mode, parent, fn)
	}
	return runTop(ctx, db, mode, fn)
}

// runTop 执行顶层事务（BEGIN/COMMIT/ROLLBACK）。
func runTop(ctx context.Context, db Beginner, mode Mode, fn func(context.Context) error) error {
	sqlDB, ok := db.(*sql.DB)
	if !ok {
		// db 不是 *sql.DB（理论上 Beginner 实现应基于 sql.DB）
		return fmt.Errorf("fusion: tx requires *sql.DB at top level, got %T", db)
	}
	sqltx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fusion: begin tx: %w", err)
	}
	f := &frame{db: sqlDB, sqlTx: sqltx, mode: mode}
	f.top = f
	childCtx := context.WithValue(ctx, txKey{}, f)

	defer func() {
		if p := recover(); p != nil {
			_ = sqltx.Rollback()
			panic(p)
		}
	}()

	if err := fn(childCtx); err != nil {
		_ = sqltx.Rollback()
		return err
	}
	if err := sqltx.Commit(); err != nil {
		return fmt.Errorf("fusion: commit tx: %w", err)
	}
	return nil
}

// runNested 执行嵌套事务（按模式 savepoint 或复用）。
func runNested(ctx context.Context, db Beginner, mode Mode, parent *frame, fn func(context.Context) error) error {
	switch mode {
	case TxModeReuse:
		return runReuse(ctx, parent, fn)
	default: // TxModeSavepoint
		return runSavepoint(ctx, parent, fn)
	}
}

// runReuse 复用外层事务，提交/回滚为 no-op。
func runReuse(ctx context.Context, parent *frame, fn func(context.Context) error) error {
	// 复用父帧的 sqlTx，标记 isNested
	f := &frame{sqlTx: parent.sqlTx, isNested: true, mode: TxModeReuse, top: parent.top}
	childCtx := context.WithValue(ctx, txKey{}, f)
	// 不 Begin/Rollback，直接执行；错误向上传递由外层决定
	return fn(childCtx)
}

// runSavepoint 用 SAVEPOINT 实现部分回滚。
func runSavepoint(ctx context.Context, parent *frame, fn func(context.Context) error) error {
	top := parent.top
	top.spMu.Lock()
	top.spCounter++
	spName := fmt.Sprintf("sp%d", top.spCounter)
	top.spMu.Unlock()

	execer := parent.sqlTx // 在事务连接上执行 SAVEPOINT
	_, err := execer.ExecContext(ctx, "SAVEPOINT "+spName)
	if err != nil {
		return fmt.Errorf("fusion: savepoint %s: %w", spName, err)
	}
	f := &frame{sqlTx: parent.sqlTx, isNested: true, mode: TxModeSavepoint, spName: spName, top: top}
	childCtx := context.WithValue(ctx, txKey{}, f)

	defer func() {
		if p := recover(); p != nil {
			_, _ = execer.ExecContext(context.Background(), "ROLLBACK TO "+spName)
			panic(p)
		}
	}()

	if err := fn(childCtx); err != nil {
		if _, rerr := execer.ExecContext(context.Background(), "ROLLBACK TO "+spName); rerr != nil {
			return fmt.Errorf("fusion: rollback to %s: %v (orig: %w)", spName, rerr, err)
		}
		return err
	}
	// 成功：RELEASE SAVEPOINT
	if _, err := execer.ExecContext(ctx, "RELEASE SAVEPOINT "+spName); err != nil {
		return fmt.Errorf("fusion: release savepoint %s: %w", spName, err)
	}
	return nil
}
