package logging

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestRenderSQL_Placeholders 验证 ? 和 $N 占位符按序替换为字面量。
func TestRenderSQL_Placeholders(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{
			name: "question_mark",
			sql:  "SELECT * FROM users WHERE id = ? AND name = ?",
			args: []any{int64(42), "alice"},
			want: "SELECT * FROM users WHERE id = 42 AND name = 'alice'",
		},
		{
			name: "pg_dollar",
			sql:  "SELECT * FROM users WHERE id = $1 AND name = $2",
			args: []any{int64(42), "alice"},
			want: "SELECT * FROM users WHERE id = 42 AND name = 'alice'",
		},
		{
			name: "in_list",
			sql:  "SELECT * FROM u WHERE id IN (?, ?, ?)",
			args: []any{int64(1), int64(2), int64(3)},
			want: "SELECT * FROM u WHERE id IN (1, 2, 3)",
		},
		{
			name: "between",
			sql:  "WHERE age BETWEEN ? AND ?",
			args: []any{int64(18), int64(65)},
			want: "WHERE age BETWEEN 18 AND 65",
		},
		{
			name: "no_args",
			sql:  "SELECT 1",
			args: nil,
			want: "SELECT 1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderSQL(c.sql, c.args)
			if got != c.want {
				t.Errorf("renderSQL:\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

// TestRenderSQL_StringLiteralNotReplaced 验证字符串字面量内的 ? 不被误替换。
func TestRenderSQL_StringLiteralNotReplaced(t *testing.T) {
	sql := "SELECT * FROM u WHERE note = 'is this?' AND id = ?"
	got := renderSQL(sql, []any{int64(7)})
	want := "SELECT * FROM u WHERE note = 'is this?' AND id = 7"
	if got != want {
		t.Errorf("string literal protection failed:\n got: %s\nwant: %s", got, want)
	}
}

// TestSqlLiteral_Types 验证各 driver.Value 形态的字面量渲染。
func TestSqlLiteral_Types(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, "NULL"},
		{"bool_true", true, "TRUE"},
		{"bool_false", false, "FALSE"},
		{"int", int(42), "42"},
		{"int64", int64(42), "42"},
		{"float64", float64(3.14), "3.14"},
		{"string", "abc", "'abc'"},
		{"string_escape", "it's", "'it''s'"},
		{"bytes", []byte{0xDE, 0xAD}, "0xdead"},
		{"time", now, "'2026-07-16T12:30:00Z'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sqlLiteral(c.v)
			if got != c.want {
				t.Errorf("sqlLiteral(%v):\n got: %s\nwant: %s", c.v, got, c.want)
			}
		})
	}
}

// TestRenderSQL_TooFewArgs 验证 args 少于占位符时占位符原样保留（容错不 panic）。
func TestRenderSQL_TooFewArgs(t *testing.T) {
	got := renderSQL("WHERE id = ? AND age = ?", []any{int64(1)})
	want := "WHERE id = 1 AND age = ?"
	if got != want {
		t.Errorf("too few args:\n got: %s\nwant: %s", got, want)
	}
}

// TestLogQuery_RenderDisabledByDefault 验证默认关闭时 sql 字段为原始模板。
func TestLogQuery_RenderDisabledByDefault(t *testing.T) {
	orig := IsRenderSQLEnabled()
	SetRenderSQL(false)
	defer SetRenderSQL(orig)

	buf, restore := captureLogger(slog.LevelDebug) // 全级别
	defer restore()
	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT * FROM u WHERE id = ?",
		Args:     []any{int64(1)},
		Duration: 1 * time.Millisecond,
	})
	out := buf.String()
	if !strings.Contains(out, "sql=\"SELECT * FROM u WHERE id = ?\"") {
		t.Errorf("render off should keep placeholder, got: %s", out)
	}
	if strings.Contains(out, "id = 1") {
		t.Errorf("render off should not substitute, got: %s", out)
	}
}

// TestLogQuery_RenderEnabled 验证全局开启时 sql 字段为组装后 SQL。
func TestLogQuery_RenderEnabled(t *testing.T) {
	orig := IsRenderSQLEnabled()
	SetRenderSQL(true)
	defer SetRenderSQL(orig)

	buf, restore := captureLogger(slog.LevelDebug)
	defer restore()
	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT * FROM u WHERE id = $1",
		Args:     []any{int64(7)},
		Duration: 1 * time.Millisecond,
	})
	out := buf.String()
	if !strings.Contains(out, "sql=\"SELECT * FROM u WHERE id = 7\"") {
		t.Errorf("render on should substitute, got: %s", out)
	}
}

// TestLogQuery_RenderCtxOverride 验证 ctx 覆盖优先于全局。
func TestLogQuery_RenderCtxOverride(t *testing.T) {
	orig := IsRenderSQLEnabled()
	SetRenderSQL(true) // 全局开
	defer SetRenderSQL(orig)

	buf, restore := captureLogger(slog.LevelDebug)
	defer restore()
	// ctx 显式关闭，覆盖全局
	ctx := WithRenderSQL(context.Background(), false)
	LogQuery(ctx, QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT * FROM u WHERE id = ?",
		Args:     []any{int64(7)},
		Duration: 1 * time.Millisecond,
	})
	out := buf.String()
	if !strings.Contains(out, "id = ?") {
		t.Errorf("ctx override off should keep placeholder, got: %s", out)
	}
}

// TestLogQuery_RenderAfterRedaction 验证敏感列值在渲染前已脱敏（组装后 SQL 不含明文）。
func TestLogQuery_RenderAfterRedaction(t *testing.T) {
	orig := IsRenderSQLEnabled()
	SetRenderSQL(true)
	defer SetRenderSQL(orig)

	buf, restore := captureLogger(slog.LevelDebug)
	defer restore()
	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT * FROM u WHERE password = ?",
		Args:     []any{"secret-pw"},
		Duration: 1 * time.Millisecond,
	})
	out := buf.String()
	// 渲染后的 sql 字段应含 '***' 而非明文
	if !strings.Contains(out, "password = '***'") {
		t.Errorf("redacted value should be rendered as '***', got: %s", out)
	}
	if strings.Contains(out, "secret-pw") {
		t.Errorf("plaintext password leaked in rendered SQL, got: %s", out)
	}
}

// TestRenderSQL_MixedDollarMultipleDigits 验证 $10 这种多位占位符。
func TestRenderSQL_MixedDollarMultipleDigits(t *testing.T) {
	sql := "INSERT INTO t VALUES ($1, $2, $10)"
	got := renderSQL(sql, []any{int64(1), int64(2), int64(10)})
	want := "INSERT INTO t VALUES (1, 2, 10)"
	if got != want {
		t.Errorf("multi-digit placeholder:\n got: %s\nwant: %s", got, want)
	}
}
