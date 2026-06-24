// Package logging 提供 SQL 执行的日志记录与查询拦截（QueryHook）。
//
// 设计要点（详见 docs/DESIGN.md 日志部分）：
//   - 门面持有 *slog.Logger（slog 在前），不自定义接口。slog 可桥接 zap/zerolog/标准库。
//   - QueryHook 是独立扩展点，供审计/慢查询/trace 拦截。
//   - 默认 logger 为 Level=Warn 的 stderr text logger（参照 GORM）——记慢查询和错误，
//     SQL 需用户调 Debug 级才看。
//   - 级别控制不在此包提供，由用户创建 slog.Logger 时设 Level（slog handler 能力）。
//
// QueryInfo 携带一次 SQL 执行的全部信息（含 RowsAffected，DML 从 sql.Result 零成本取）。
package logging

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// QueryInfo 携带一次 SQL 执行的全部信息，传给 QueryHook 并用于日志记录。
type QueryInfo struct {
	// Op 操作类型："SELECT"/"INSERT"/"UPDATE"/"DELETE"。调用点显式传，保证准确。
	Op string
	// SQL 完整 SQL 语句。
	SQL string
	// Args SQL 参数（按顺序）。
	Args []any
	// Duration 执行耗时。
	Duration time.Duration
	// RowsAffected 受影响/返回行数。
	//   - DML：来自 sql.Result.RowsAffected()（O(1)）
	//   - SELECT All：结果切片长度
	//   - SELECT One：命中填 1，ErrNoRows 填 0
	//   - SELECT Count：1
	RowsAffected int64
	// Err 执行错误，nil 表示成功。
	Err error
}

// QueryHook 查询拦截器。在每个 SQL 执行完成后被调用，可拿到完整 QueryInfo。
// 返回 error 会向上传递（不常见，主要用于拦截式审计）。
type QueryHook func(ctx context.Context, info QueryInfo) error

var (
	mu      sync.RWMutex
	logger  = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hooks   []QueryHook
	slow    = 200 * time.Millisecond
	slowMu  sync.RWMutex
)

// SetLogger 设置全局 slog.Logger（带 RWMutex，参照 tx/hook 包）。
// 传入 nil 等同于丢弃所有日志（用 slog.New(discardHandler)）。
func SetLogger(l *slog.Logger) {
	mu.Lock()
	defer mu.Unlock()
	if l == nil {
		l = slog.New(discardHandler{})
	}
	logger = l
}

// Logger 返回当前全局 logger（带 RLock）。
func Logger() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

// AddQueryHook 注册一个查询拦截器，返回注销函数（参照 hook.Register 的模式）。
func AddQueryHook(h QueryHook) (unregister func()) {
	mu.Lock()
	defer mu.Unlock()
	hooks = append(hooks, h)
	idx := len(hooks) - 1
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if idx < len(hooks) {
			hooks[idx] = nil // 标记移除，保持索引稳定
		}
	}
}

// SetSlowThreshold 设置慢查询阈值（默认 200ms）。超过则 Warn 级记录。
func SetSlowThreshold(d time.Duration) {
	slowMu.Lock()
	defer slowMu.Unlock()
	slow = d
}

// SlowThreshold 返回当前慢查询阈值。
func SlowThreshold() time.Duration {
	slowMu.RLock()
	defer slowMu.RUnlock()
	return slow
}

// LogQuery 统一入口：触发所有 QueryHook 并记录 slog。
// 在每个执行点拿到 err/耗时/行数后调用。
func LogQuery(ctx context.Context, info QueryInfo) {
	// 1. 触发 QueryHook（RLock 下复制切片，执行时不持锁，参照 hook.go）
	mu.RLock()
	cb := make([]QueryHook, len(hooks))
	copy(cb, hooks)
	lg := logger
	mu.RUnlock()
	for _, h := range cb {
		if h == nil {
			continue
		}
		// 注：QueryHook 的 error 不影响日志记录本身，向上传递由调用方决定
		_ = h(ctx, info)
	}

	// 2. 记录 slog（按内容选级别）
	attrs := []any{
		slog.String("op", info.Op),
		slog.String("sql", info.SQL),
		slog.Any("args", info.Args),
		slog.Duration("duration", info.Duration),
		slog.Int64("rows", info.RowsAffected),
	}
	switch {
	case info.Err != nil:
		lg.Error("query failed", append(attrs, slog.Any("err", info.Err))...)
	case info.Duration >= SlowThreshold():
		lg.Warn("slow query", attrs...)
	default:
		lg.Debug("query", attrs...)
	}
}

// discardHandler 丢弃所有日志的 slog.Handler（用于 SetLogger(nil) 或静默场景）。
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool      { return false }
func (discardHandler) Handle(context.Context, slog.Record) error     { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler          { return h }
func (h discardHandler) WithGroup(string) slog.Handler               { return h }
