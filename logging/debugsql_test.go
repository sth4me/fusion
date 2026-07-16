package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestDebugSQLHandler_ZeroEscaping 验证含 PG 双引号标识符的 SQL 原样输出、
// 无 slog 转义（无 \" 包裹），可直接复制粘贴执行。
func TestDebugSQLHandler_ZeroEscaping(t *testing.T) {
	buf := &bytes.Buffer{}
	h := NewDebugSQLHandler(buf, slog.LevelDebug)
	logger := slog.New(h)

	// 模拟 LogQuery 记录的属性结构（sql 模板 + args + op + duration + rows）
	logger.Log(context.Background(), slog.LevelDebug, "query",
		slog.String("op", "SELECT"),
		slog.String("sql", "SELECT \"user_id\", \"role_id\" FROM \"user_roles\" WHERE \"user_id\" = $1"),
		slog.Any("args", []any{"019f63e0-3bf7-7488-91c0-c67f955f2753"}),
		slog.Duration("duration", 2100*time.Microsecond),
		slog.Int64("rows", 1),
	)

	out := buf.String()
	// 关键断言：SQL 含原样双引号、无反斜杠转义、无外层包裹引号
	if !strings.Contains(out, `"user_id", "role_id"`) {
		t.Errorf("PG double-quote identifiers should be preserved verbatim, got: %s", out)
	}
	if strings.Contains(out, `\"`) {
		t.Errorf("no backslash escaping expected, got: %s", out)
	}
	// 占位符 $1 应被渲染为字面量
	if !strings.Contains(out, `"user_id" = '019f63e0-3bf7-7488-91c0-c67f955f2753'`) {
		t.Errorf("placeholder should be rendered as literal, got: %s", out)
	}
}

// TestDebugSQLHandler_LevelFilter 验证低于设定级别的日志被丢弃。
func TestDebugSQLHandler_LevelFilter(t *testing.T) {
	buf := &bytes.Buffer{}
	// 只记 WARN 及以上
	h := NewDebugSQLHandler(buf, slog.LevelWarn)
	logger := slog.New(h)

	logger.Log(context.Background(), slog.LevelDebug, "query", slog.String("sql", "SELECT 1"))
	if buf.Len() != 0 {
		t.Errorf("Debug below Warn threshold should be dropped, got: %s", buf.String())
	}

	logger.Log(context.Background(), slog.LevelWarn, "slow query", slog.String("sql", "SELECT 2"))
	if buf.Len() == 0 {
		t.Error("Warn should be recorded")
	}
}

// TestDebugSQLHandler_ErrorAnnotation 验证错误日志追加 err=。
func TestDebugSQLHandler_ErrorAnnotation(t *testing.T) {
	buf := &bytes.Buffer{}
	h := NewDebugSQLHandler(buf, slog.LevelDebug)
	logger := slog.New(h)

	logger.Log(context.Background(), slog.LevelError, "query failed",
		slog.String("op", "INSERT"),
		slog.String("sql", "INSERT INTO u (name) VALUES (?)"),
		slog.Any("args", []any{"alice"}),
		slog.Duration("duration", time.Millisecond),
		slog.Int64("rows", 0),
		slog.Any("err", "duplicate key"),
	)
	out := buf.String()
	if !strings.Contains(out, "err=duplicate key") {
		t.Errorf("error should be annotated, got: %s", out)
	}
	if !strings.Contains(out, "VALUES ('alice')") {
		t.Errorf("INSERT placeholder should be rendered, got: %s", out)
	}
}

// TestDebugSQLHandler_NoArgs 验证无参数 SQL 原样输出不报错。
func TestDebugSQLHandler_NoArgs(t *testing.T) {
	buf := &bytes.Buffer{}
	h := NewDebugSQLHandler(buf, slog.LevelDebug)
	logger := slog.New(h)

	logger.Log(context.Background(), slog.LevelDebug, "query",
		slog.String("sql", "SELECT NOW()"),
		slog.Duration("duration", time.Microsecond),
		slog.Int64("rows", 1),
	)
	out := buf.String()
	if !strings.Contains(out, "SELECT NOW()") {
		t.Errorf("argless SQL should pass through, got: %s", out)
	}
}

// TestDebugSQLHandler_IntegrationWithLogQuery 端到端：挂 handler 走 LogQuery，
// 验证真实链路下输出零转义可粘贴。
func TestDebugSQLHandler_IntegrationWithLogQuery(t *testing.T) {
	origLogger := Logger()
	buf := &bytes.Buffer{}
	SetLogger(slog.New(NewDebugSQLHandler(buf, slog.LevelDebug)))
	defer SetLogger(origLogger)

	// 确保全局渲染开关关闭，证明 handler 自带渲染不依赖它
	origRender := IsRenderSQLEnabled()
	SetRenderSQL(false)
	defer SetRenderSQL(origRender)

	LogQuery(context.Background(), QueryInfo{
		Op:       "SELECT",
		SQL:      "SELECT \"id\" FROM \"users\" WHERE \"id\" = $1",
		Args:     []any{int64(42)},
		Duration: 2 * time.Millisecond,
		RowsAffected: 1,
	})
	out := buf.String()
	if !strings.Contains(out, `WHERE "id" = 42`) {
		t.Errorf("integration: rendered SQL should have literal, got: %s", out)
	}
	if strings.Contains(out, `\"`) {
		t.Errorf("integration: no escaping expected, got: %s", out)
	}
}
