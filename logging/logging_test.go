package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// --- 辅助：捕获 slog 输出的 handler ---

// captureLogger 创建一个写 *bytes.Buffer 的 logger，并替换全局 logger。
// 返回 buffer 和 恢复函数。level 控制记录级别。
func captureLogger(level slog.Level) (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	origLogger := Logger()
	SetLogger(slog.New(h))
	return buf, func() { SetLogger(origLogger) }
}

// --- QueryHook 测试 ---

func TestAddQueryHook(t *testing.T) {
	var got []QueryInfo
	unreg := AddQueryHook(func(ctx context.Context, info QueryInfo) error {
		got = append(got, info)
		return nil
	})
	defer unreg()

	info := QueryInfo{Op: "SELECT", SQL: "SELECT 1", Args: []any{}, Duration: 1 * time.Millisecond, RowsAffected: 1}
	LogQuery(context.Background(), info)

	if len(got) != 1 {
		t.Fatalf("got %d hook calls, want 1", len(got))
	}
	if got[0].Op != "SELECT" || got[0].SQL != "SELECT 1" || got[0].RowsAffected != 1 {
		t.Errorf("hook got %+v", got[0])
	}
}

func TestQueryHookUnregister(t *testing.T) {
	called := false
	unreg := AddQueryHook(func(ctx context.Context, info QueryInfo) error {
		called = true
		return nil
	})
	LogQuery(context.Background(), QueryInfo{Op: "SELECT", SQL: "x"})
	if !called {
		t.Error("hook should fire before unregister")
	}

	called = false
	unreg()
	LogQuery(context.Background(), QueryInfo{Op: "SELECT", SQL: "x"})
	if called {
		t.Error("hook should NOT fire after unregister")
	}
}

func TestQueryHookMultiple(t *testing.T) {
	var order []string
	u1 := AddQueryHook(func(ctx context.Context, info QueryInfo) error {
		order = append(order, "first")
		return nil
	})
	defer u1()
	u2 := AddQueryHook(func(ctx context.Context, info QueryInfo) error {
		order = append(order, "second")
		return nil
	})
	defer u2()

	LogQuery(context.Background(), QueryInfo{Op: "SELECT", SQL: "x"})
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("order got %v, want [first second]", order)
	}
}

// --- slog 级别测试 ---

func TestLoggerDefault(t *testing.T) {
	l := Logger()
	if l == nil {
		t.Fatal("default logger should not be nil")
	}
	// 默认 logger 是 Warn 级（参照 GORM）。验证 Enabled(Warn)=true, Enabled(Debug)=false
	ctx := context.Background()
	if !l.Enabled(ctx, slog.LevelWarn) {
		t.Error("default logger should be Warn-enabled")
	}
	if l.Enabled(ctx, slog.LevelDebug) {
		t.Error("default logger should NOT be Debug-enabled")
	}
}

func TestSetLogger(t *testing.T) {
	orig := Logger()
	defer SetLogger(orig)

	newL := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	SetLogger(newL)
	if Logger() != newL {
		t.Error("Logger() should return the newly set logger")
	}

	// SetLogger(nil) 应退化为 discard handler（不 panic、Enabled=false）
	SetLogger(nil)
	nilL := Logger()
	if nilL.Enabled(context.Background(), slog.LevelError) {
		t.Error("nil logger should be discard (Enabled=false)")
	}
}

func TestQueryError(t *testing.T) {
	buf, restore := captureLogger(slog.LevelError)
	defer restore()

	LogQuery(context.Background(), QueryInfo{
		Op:  "UPDATE",
		SQL: "UPDATE users SET x=1",
		Err: errors.New("db down"),
		Duration: 1 * time.Millisecond,
	})
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("should log ERROR level, got %s", out)
	}
	if !strings.Contains(out, "query failed") {
		t.Errorf("should contain 'query failed', got %s", out)
	}
	if !strings.Contains(out, "db down") {
		t.Errorf("should contain err, got %s", out)
	}
}

func TestSlowQuery(t *testing.T) {
	// 改小阈值确保触发
	origSlow := SlowThreshold()
	SetSlowThreshold(1 * time.Millisecond)
	defer func() {
		SetSlowThreshold(origSlow)
	}()

	buf, restore := captureLogger(slog.LevelWarn)
	defer restore()

	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT * FROM big_table",
		Duration: 5 * time.Millisecond, // 超过 1ms 阈值
	})
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("slow query should be WARN, got %s", out)
	}
	if !strings.Contains(out, "slow query") {
		t.Errorf("should contain 'slow query', got %s", out)
	}
}

func TestQueryDebug(t *testing.T) {
	// 正常查询（无错、不慢）应记 Debug 级。需 handler 设 Debug 才出现。
	buf, restore := captureLogger(slog.LevelDebug)
	defer restore()

	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT 1",
		Duration: 0,
	})
	out := buf.String()
	if !strings.Contains(out, "level=DEBUG") {
		t.Errorf("normal query should be DEBUG, got %s", out)
	}
}

func TestSlowThreshold(t *testing.T) {
	origSlow := SlowThreshold()
	defer SetSlowThreshold(origSlow)

	SetSlowThreshold(500 * time.Millisecond)
	if got := SlowThreshold(); got != 500*time.Millisecond {
		t.Errorf("got %v, want 500ms", got)
	}
}

func TestQueryInfoFields(t *testing.T) {
	// 验证 RowsAffected/Args/Op 等字段在日志中正确出现
	buf, restore := captureLogger(slog.LevelDebug)
	defer restore()

	LogQuery(context.Background(), QueryInfo{
		Op:           "INSERT",
		SQL:          "INSERT INTO t VALUES (?)",
		Args:         []any{"alice", 30},
		Duration:     2 * time.Millisecond,
		RowsAffected: 1,
	})
	out := buf.String()
	for _, want := range []string{
		`op=INSERT`, `sql="INSERT INTO t VALUES (?)"`, `rows=1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log should contain %q, got %s", want, out)
		}
	}
}

// 并发安全：SetLogger 与 Logger 并发不 panic
func TestConcurrentSetLogger(t *testing.T) {
	orig := Logger()
	defer SetLogger(orig)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = Logger()
		}
	}()
	for i := 0; i < 100; i++ {
		SetLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	}
	<-done
}

// TestMapPlaceholdersToColumns 验证占位符→列名映射。
func TestMapPlaceholdersToColumns(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{`SELECT * FROM users WHERE password = ? AND name = ?`, []string{"password", "name"}},
		{`WHERE id IN (?, ?, ?)`, []string{"id", "id", "id"}},
		{`WHERE age BETWEEN ? AND ?`, []string{"age", "age"}},
		{`WHERE token LIKE ?`, []string{"token"}},
	}
	for _, c := range cases {
		got := mapPlaceholdersToColumns(c.sql)
		if len(got) != len(c.want) {
			t.Errorf("sql %q: got %v, want %v", c.sql, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("sql %q: pos %d got %q, want %q", c.sql, i, got[i], c.want[i])
			}
		}
	}
}

// TestRedactArgs 验证敏感列参数被替换为 ***，普通列不动。
func TestRedactArgs(t *testing.T) {
	AddSensitiveColumn("password")
	args := []any{"secret-pw", "alice"}
	got := redactArgs(`WHERE password = ? AND name = ?`, args)
	if got[0] != "***" {
		t.Errorf("password arg should be redacted, got %v", got[0])
	}
	if got[1] != "alice" {
		t.Errorf("name arg should NOT be redacted, got %v", got[1])
	}

	// IN 列表全脱敏
	args2 := []any{"t1", "t2"}
	got2 := redactArgs(`WHERE token IN (?, ?)`, args2)
	if got2[0] != "***" || got2[1] != "***" {
		t.Errorf("token IN args should both be redacted, got %v", got2)
	}
}

// TestRedactionDisable 验证 SetRedactionEnabled(false) 关闭脱敏。
func TestRedactionDisable(t *testing.T) {
	AddSensitiveColumn("password")
	SetRedactionEnabled(false)
	defer SetRedactionEnabled(true)
	got := redactArgs(`WHERE password = ?`, []any{"pw"})
	if got[0] != "pw" {
		t.Errorf("redaction disabled: arg should be original, got %v", got[0])
	}
}
