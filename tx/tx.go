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
	"math/rand"
	"strings"
	"sync"
	"time"
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
//
// 等价于 TxWithOpts(ctx, db, mode, nil, fn)。需要隔离级别/只读/死锁重试，用 TxWithOpts。
func Tx(ctx context.Context, db Beginner, mode Mode, fn func(ctx context.Context) error) error {
	return TxWithOpts(ctx, db, mode, nil, fn)
}

// Options 控制顶层事务的隔离级别、只读标志与死锁重试策略。
// 仅对顶层事务生效；嵌套（savepoint/reuse）不重试。
type Options struct {
	// TxOptions 透传给 database/sql 的 BeginTx（隔离级别、只读）。
	TxOptions *sql.TxOptions
	// RetryDeadlocks 为死锁/序列化失败的最大重试次数（0=不重试）。
	RetryDeadlocks int
	// RetryBaseDelay 重试初始退避（默认 5ms）。
	RetryBaseDelay time.Duration
	// RetryMaxDelay 重试最大退避（默认 100ms）。
	RetryMaxDelay time.Duration
}

// Option 是配置 Options 的函数式选项（供 fusion 层透传）。
type Option func(*Options)

// TxWithOpts 同 Tx，但支持隔离级别与死锁重试。opts 为 nil 时等价于 Tx。
//
// 重试语义：仅当 fn 执行期间发生可重试错误（PG 40P01/40P02/40001，
// MySQL 1213/1205，或错误信息含 "deadlock"/"try restarting transaction"）时，
// 按 RetryDeadlocks 次数指数退避重试整个事务。fn 必须幂等（重试会重复执行）。
// begin/commit 阶段的错误不重试。
func TxWithOpts(ctx context.Context, db Beginner, mode Mode, opts *Options, fn func(ctx context.Context) error) error {
	if mode == 0 {
		mode = DefaultMode()
	}

	// 检测外层事务（嵌套不重试，opts 的隔离级别也只对顶层生效）
	parent, hasParent := ctx.Value(txKey{}).(*frame)
	if hasParent && parent != nil {
		return runNested(ctx, db, mode, parent, fn)
	}

	if opts == nil || opts.RetryDeadlocks <= 0 {
		return runTop(ctx, db, mode, opts, fn, 0)
	}
	base := opts.RetryBaseDelay
	if base <= 0 {
		base = 5 * time.Millisecond
	}
	maxD := opts.RetryMaxDelay
	if maxD <= 0 {
		maxD = 100 * time.Millisecond
	}
	var lastErr error
	for attempt := 0; attempt <= opts.RetryDeadlocks; attempt++ {
		err := runTop(ctx, db, mode, opts, fn, attempt)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableTxError(err) {
			return err
		}
		if attempt < opts.RetryDeadlocks {
			// 指数退避 + jitter（避免惊群）
			backoff := base * (1 << attempt)
			if backoff > maxD {
				backoff = maxD
			}
			jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff/2 + jitter):
			}
		}
	}
	return fmt.Errorf("fusion: tx failed after %d retries: %w", opts.RetryDeadlocks, lastErr)
}

// runTop 执行顶层事务（BEGIN/COMMIT/ROLLBACK）。
// attempt 仅用于日志/调试，0=首次。
//
// db 需实现 Beginner（BeginTx + ExecContext）；*sql.DB 满足，便于测试注入 stub。
func runTop(ctx context.Context, db Beginner, mode Mode, opts *Options, fn func(context.Context) error, attempt int) error {
	_ = attempt
	var txOpts *sql.TxOptions
	if opts != nil {
		txOpts = opts.TxOptions
	}
	sqltx, err := db.BeginTx(ctx, txOpts)
	if err != nil {
		return fmt.Errorf("fusion: begin tx: %w", err)
	}
	f := &frame{sqlTx: sqltx, mode: mode}
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

// isRetryableTxError 判断错误是否为可重试的事务错误（死锁/序列化失败）。
// 通过字符串匹配识别 PG/MySQL 的 SQLState，避免引入驱动依赖。
// isRetryableTxError 判断错误是否为可重试的事务错误（死锁/序列化失败）。
//
// 用带语义上下文的字符串匹配，避免裸数字（"1213"/"1205"）误判端口号/耗时/ID 等无关文本。
// 理想方案是驱动 typed error（pgconn.PgError.SQLState / mysql.MySQLError.Number），
// 但为避免引入驱动依赖，当前覆盖主流驱动的错误文本形态：
//   - PG:   "SQLSTATE 40P01"、"ERROR: deadlock detected"、"could not serialize access"
//   - MySQL:"Error 1213"、"Deadlock found"、"try restarting transaction"、"Lock wait timeout"
// IsRetryableError 报告错误是否为可重试的事务错误（死锁/序列化失败）。
// 导出版本，供应用层判断是否值得重试（fusion.TxWithOpts 内部也用此判断）。
func IsRetryableError(err error) bool { return isRetryableTxError(err) }

func isRetryableTxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	retryable := []string{
		"40P01", "40P02", "40001", // PG SQLSTATE（5 位码，误判概率极低）
		"Error 1213",         // MySQL deadlock（带 "Error " 前缀）
		"Error 1205",         // MySQL lock wait timeout
		"deadlock detected",  // PG 文本
		"Deadlock found",     // MySQL 文本
		"try restarting transaction",
		"could not serialize access",
		"serialization failure",
		"Lock wait timeout exceeded",
	}
	for _, s := range retryable {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
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
