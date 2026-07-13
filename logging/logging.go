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
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sth4me/fusion/expr"
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

	// sensitiveCols 需脱敏的列名集合（小写匹配）。默认含常见敏感列名。
	sensMu         sync.RWMutex
	sensitiveCols  = map[string]bool{
		"password": true, "passwd": true, "secret": true, "token": true,
		"api_key": true, "apikey": true, "access_token": true, "refresh_token": true,
		"private_key": true, "privatekey": true, "credential": true, "credentials": true,
	}
	redactionEnabled = true
)

// AddSensitiveColumn 追加需脱敏的列名（大小写不敏感）。
// 匹配到的列对应的参数值在日志中替换为 "***"。
func AddSensitiveColumn(names ...string) {
	sensMu.Lock()
	defer sensMu.Unlock()
	for _, n := range names {
		sensitiveCols[strings.ToLower(n)] = true
	}
}

// SetSensitiveColumns 设置（覆盖）脱敏列名集合。传 nil 清空（关闭按列脱敏）。
func SetSensitiveColumns(names []string) {
	sensMu.Lock()
	defer sensMu.Unlock()
	sensitiveCols = make(map[string]bool, len(names))
	for _, n := range names {
		sensitiveCols[strings.ToLower(n)] = true
	}
}

// SetRedactionEnabled 开关按列脱敏（默认开）。关闭后日志原样输出参数。
func SetRedactionEnabled(enabled bool) {
	sensMu.Lock()
	defer sensMu.Unlock()
	redactionEnabled = enabled
}

// isSensitiveColumn 报告列名是否在脱敏集合中（大小写不敏感）。
func isSensitiveColumn(col string) bool {
	sensMu.RLock()
	defer sensMu.RUnlock()
	if !redactionEnabled {
		return false
	}
	return sensitiveCols[strings.ToLower(col)]
}

// redactArgs 把 info.Args 中对应敏感列的值替换为 "***"。
// 通过扫描 SQL 把每个占位符（? 或 $N）映射到其列名，再判断是否敏感。
// 覆盖常见模式：col OP ?、col IN/BETWEEN ?。INSERT VALUES 的列映射较复杂，
// 当前按"VALUES 区域内无法可靠定位列"保守不脱敏（用户可用 AddQueryHook 自行处理）。
func redactArgs(sqlStr string, args []any) []any {
	if len(args) == 0 {
		return args
	}
	colAt := mapPlaceholdersToColumns(sqlStr) // [argIdx] -> colName
	out := make([]any, len(args))
	copy(out, args)
	for i, col := range colAt {
		if i < len(out) && isSensitiveColumn(col) {
			out[i] = "***"
		}
	}
	return out
}

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
//
// 优先使用 ctx 携带的 per-Engine 覆盖（logger/slow 阈值），无则用全局。
// 这样多 Engine 场景能各自记不同 logger；全局 API（无 Engine）行为不变。
func LogQuery(ctx context.Context, info QueryInfo) {
	// 1. 触发 QueryHook（RLock 下复制切片，执行时不持锁，参照 hook.go）
	mu.RLock()
	cb := make([]QueryHook, len(hooks))
	copy(cb, hooks)
	lg := logger
	mu.RUnlock()
	// ctx 携带的 per-call 覆盖（Engine 注入 logger；slow 阈值后面单独取）
	if ol, _, ok := loggerFromCtx(ctx); ok {
		if ol != nil {
			lg = ol
		}
	}
	for _, h := range cb {
		if h == nil {
			continue
		}
		// 注：QueryHook 的 error 不影响日志记录本身，向上传递由调用方决定
		_ = h(ctx, info)
	}

	// 2. 记录 slog（按内容选级别）
	// 敏感字段脱敏：把敏感列对应的参数值替换为 "***"（仅影响日志输出，不改原 Args）
	logArgs := redactArgs(info.SQL, info.Args)
	attrs := []any{
		slog.String("op", info.Op),
		slog.String("sql", info.SQL),
		slog.Any("args", logArgs),
		slog.Duration("duration", info.Duration),
		slog.Int64("rows", info.RowsAffected),
	}
	// 慢查询阈值：优先 ctx 覆盖，无则全局
	slowThreshold := SlowThreshold()
	if _, sc, ok := loggerFromCtx(ctx); ok && sc >= 0 {
		slowThreshold = sc
	}
	switch {
	case info.Err != nil && !isNoRowsErr(info.Err):
		// 真错误（连接/语法/约束冲突等）才记 ERROR。
		// sql.ErrNoRows / fusion.ErrNotFound 是业务正常路径（查无结果），降级 Debug。
		lg.Error("query failed", append(attrs, slog.Any("err", info.Err))...)
	case info.Err != nil:
		// 查询无结果：业务正常路径，Debug 级（默认不输出，调试时可开 Debug level）。
		lg.Debug("query no rows", attrs...)
	case info.Duration >= slowThreshold:
		lg.Warn("slow query", attrs...)
	default:
		lg.Debug("query", attrs...)
	}
}

// ctxLoggerKey 是 context 中 per-Engine logger/slow 覆盖的 key。
type ctxLoggerKey struct{}

// ctxOverride 携带 Engine 注入的 logger 和 slow 阈值（slow<0 表示用全局）。
type ctxOverride struct {
	logger *slog.Logger
	slow   time.Duration
}

// WithOverride 把 logger/slow 覆盖挂到 ctx，返回新 ctx。
// 供 Engine 在执行查询前注入，实现 per-Engine 日志/慢阈值。
// lg 为 nil 表示沿用全局 logger；slow < 0 表示沿用全局阈值。
func WithOverride(ctx context.Context, lg *slog.Logger, slow time.Duration) context.Context {
	return context.WithValue(ctx, ctxLoggerKey{}, ctxOverride{logger: lg, slow: slow})
}

// loggerFromCtx 从 ctx 取 per-Engine 覆盖（若有）。
func loggerFromCtx(ctx context.Context) (*slog.Logger, time.Duration, bool) {
	if o, ok := ctx.Value(ctxLoggerKey{}).(ctxOverride); ok {
		return o.logger, o.slow, true
	}
	return nil, 0, false
}

// discardHandler 丢弃所有日志的 slog.Handler（用于 SetLogger(nil) 或静默场景）。
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool      { return false }
func (discardHandler) Handle(context.Context, slog.Record) error     { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler          { return h }
func (h discardHandler) WithGroup(string) slog.Handler               { return h }

// isNoRowsErr 报告 err 是否为"查询无结果"类（sql.ErrNoRows，含 fusion.ErrNotFound 包装）。
// 这类是业务正常路径（One 无匹配），不应记 ERROR。用 sql.ErrNoRows 判即可——
// fusion 的 ErrNotFound 用 %w 双重包装了 sql.ErrNoRows，errors.Is 兼容。
func isNoRowsErr(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// mapPlaceholdersToColumns 扫描 SQL，返回每个占位符（按出现顺序）对应的列名。
// 无法定位的占位符对应空串（不脱敏）。
//
// 支持的模式：
//   - col OP ?          （OP ∈ =, <>, !=, >, >=, <, <=, LIKE, IS）
//   - col IN (?, ?, ...)（区间内所有 ? 都归到 col）
//   - col BETWEEN ? AND ?
//   - INSERT INTO t (col1, col2, ...) VALUES (?, ?), (?, ?)...（按列位置循环映射）
func mapPlaceholdersToColumns(sqlStr string) []string {
	// INSERT 路径：解析列列表 + VALUES 区域，按位置循环映射占位符到列名
	if cols, ok := parseInsertColumns(sqlStr); ok {
		return mapInsertPlaceholders(sqlStr, cols)
	}
	var out []string
	low := strings.ToLower(sqlStr)
	i := 0
	n := len(sqlStr)
	currentCol := "" // 当前生效的列名上下文
	for i < n {
		// 跳过字符串字面量/注释（其内的 ?/$N 不是占位符，也不影响列上下文）
		if next := expr.SkipNonCode(sqlStr, i); next > i {
			i = next
			continue
		}
		ch := sqlStr[i]
		// 占位符 ? 或 $N
		if ch == '?' {
			out = append(out, currentCol)
			i++
			continue
		}
		if ch == '$' && i+1 < n && sqlStr[i+1] >= '1' && sqlStr[i+1] <= '9' {
			j := i + 1
			for j < n && sqlStr[j] >= '0' && sqlStr[j] <= '9' {
				j++
			}
			out = append(out, currentCol)
			i = j
			continue
		}
		// 识别标识符 token（字母/下划线开头，含字母数字下划线），可能带点（table.col）
		if isIdentStart(ch) {
			j := i
			for j < n && isIdentPart(sqlStr[j]) {
				j++
			}
			tokLow := strings.ToLower(low[i:j])
			// 跳过关键字（它们不是列名）
			if isSQLKeyword(tokLow) {
				// 特殊：IN / BETWEEN 后的列上下文保持（col IN (...)）
				i = j
				continue
			}
			// 这是一个列名候选。检查后续是否紧跟 OP / IN / BETWEEN。
			// 跳过空白
			k := j
			for k < n && (sqlStr[k] == ' ' || sqlStr[k] == '\t' || sqlStr[k] == '\n' || sqlStr[k] == '\r') {
				k++
			}
			if k < n {
				nextLow := strings.ToLower(low[k:])
				if hasPrefixAny(nextLow, "=", "<>", "!=", ">=", "<=", ">", "<") ||
					strings.HasPrefix(nextLow, "like ") || strings.HasPrefix(nextLow, "is ") ||
					strings.HasPrefix(nextLow, "in ") || strings.HasPrefix(nextLow, "in(") ||
					strings.HasPrefix(nextLow, "between ") {
					// 该列名后接比较/IN/BETWEEN/LIKE/IS → 设为当前列上下文
					currentCol = tokLow
					i = j
					continue
				}
			}
			// 非列名上下文：清空（避免上一个列串到无关占位符）
			currentCol = ""
			i = j
			continue
		}
		// 其他字符（标点/空白）：逗号、右括号等会"断开"列上下文（IN 列表结束时）
		if ch == ',' || ch == ')' {
			// IN (?, ?, ?) 的逗号之间保持列上下文；右括号结束
			if ch == ')' {
				currentCol = ""
			}
			// 逗号：若在 IN 列表内，保持 currentCol；否则清空。简单处理：保留（IN 场景）
		}
		i++
	}
	return out
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.'
}

// isSQLKeyword 报告 token 是否为不应作为列名的 SQL 关键字。
func isSQLKeyword(tok string) bool {
	switch tok {
	case "select", "from", "where", "and", "or", "not", "in", "between",
		"like", "is", "null", "values", "insert", "into", "update", "set",
		"delete", "join", "on", "as", "order", "by", "group", "having",
		"limit", "offset", "distinct", "case", "when", "then", "else", "end":
		return true
	}
	return false
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// parseInsertColumns 尝试从 `INSERT INTO <table> (col1, col2, ...) VALUES` 中
// 解析出列名列表。匹配返回 (cols, true)；否则 (nil, false)。
// 仅识别显式列列表形式；`INSERT INTO t VALUES`（无列列表）返回 false。
func parseInsertColumns(sqlStr string) ([]string, bool) {
	low := strings.ToLower(sqlStr)
	// 找 "insert into"
	idx := strings.Index(low, "insert into")
	if idx != 0 {
		return nil, false
	}
	// 跳过 "insert into" + 表名（到第一个 '(' ）
	i := idx + len("insert into")
	for i < len(sqlStr) && sqlStr[i] != '(' {
		i++
	}
	if i >= len(sqlStr) {
		return nil, false
	}
	// 解析 () 内的列名（逗号分隔，去空白）
	i++ // 跳过 '('
	colStart := i
	var cols []string
	depth := 0
	for i < len(sqlStr) {
		// 跳过字面量/注释（避免表名带引号等干扰）
		if next := expr.SkipNonCode(sqlStr, i); next > i {
			i = next
			continue
		}
		c := sqlStr[i]
		if c == '(' {
			depth++
		} else if c == ')' {
			if depth == 0 {
				// 列列表结束
				if colStart < i {
					cols = append(cols, strings.TrimSpace(sqlStr[colStart:i]))
				}
				return cols, len(cols) > 0
			}
			depth--
		} else if c == ',' && depth == 0 {
			cols = append(cols, strings.TrimSpace(sqlStr[colStart:i]))
			colStart = i + 1
		}
		i++
	}
	return nil, false
}

// mapInsertPlaceholders 在 INSERT 语句的 VALUES 区域内，按列位置循环把占位符映射到列名。
// 形如 INSERT INTO t (a, b) VALUES (?, ?), (?, ?) → [a, b, a, b]。
// VALUES 区域以 "values" 关键字后开始；非占位符的字面量（如 NULL、数字）跳过但不占列位。
func mapInsertPlaceholders(sqlStr string, cols []string) []string {
	low := strings.ToLower(sqlStr)
	vIdx := strings.Index(low, " values")
	if vIdx < 0 {
		vIdx = strings.Index(low, ") values")
	}
	if vIdx < 0 {
		return nil
	}
	// 从 values 后开始扫描占位符
	i := vIdx + 7
	if i > len(sqlStr) {
		i = len(sqlStr)
	}
	var out []string
	colPos := 0 // 当前占位符在列列表中的位置（循环）
	for i < len(sqlStr) {
		if next := expr.SkipNonCode(sqlStr, i); next > i {
			i = next
			continue
		}
		c := sqlStr[i]
		if c == '?' {
			out = append(out, cols[colPos%len(cols)])
			colPos++
			i++
			continue
		}
		if c == '$' && i+1 < len(sqlStr) && sqlStr[i+1] >= '1' && sqlStr[i+1] <= '9' {
			j := i + 1
			for j < len(sqlStr) && sqlStr[j] >= '0' && sqlStr[j] <= '9' {
				j++
			}
			out = append(out, cols[colPos%len(cols)])
			colPos++
			i = j
			continue
		}
		i++
	}
	return out
}
