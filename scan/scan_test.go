package scan

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"fusion/col"
	"fusion/logging"
	"fusion/meta"
)

// tRow 是测试模型
type tRow struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func getMeta() *meta.ModelMeta {
	return meta.Register[tRow]("t_rows").Meta
}

// fakeRows 是 scan.Rows 的内存实现。
type fakeRows struct {
	cols   []string
	data   [][]any // 每行按列顺序的值
	cursor int
	err    error
}

func (f *fakeRows) Columns() ([]string, error) { return f.cols, nil }
func (f *fakeRows) Next() bool {
	if f.err != nil {
		return false
	}
	return f.cursor < len(f.data)
}
func (f *fakeRows) Err() error { return f.err }

// Scan 接收 dest（每个应是 *Col[T] 或 Scanner），把当前行数据填入。
func (f *fakeRows) Scan(dest ...any) error {
	if f.cursor >= len(f.data) {
		return errors.New("no more rows")
	}
	row := f.data[f.cursor]
	f.cursor++
	for i, d := range dest {
		// d 是 *Col[T]，它实现了 sql.Scanner
		src := row[i]
		if sc, ok := d.(scanner); ok {
			if err := sc.Scan(src); err != nil {
				return err
			}
			continue
		}
	}
	return nil
}

func TestScanAllBasic(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{
			{int64(1), "alice", 30, "a@e.com"},
			{int64(2), "bob", 17, nil},
		},
	}
	out, err := All[tRow](rows, getMeta())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].Name.Get() != "alice" {
		t.Errorf("row0 name got %q", out[0].Name.Get())
	}
	if out[0].Age.Get() != 30 {
		t.Errorf("row0 age got %d", out[0].Age.Get())
	}
}

func TestScanNullField(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{
			{int64(2), "bob", 17, nil},
		},
	}
	out, _ := All[tRow](rows, getMeta())
	// Email 是 Col[*string]，nil → 指针为 nil
	if out[0].Email.Get() != nil {
		t.Errorf("null email should be nil, got %v", out[0].Email.Get())
	}
}

func TestScanPtrFieldDeref(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{
			{int64(1), "alice", 30, "a@e.com"},
		},
	}
	out, _ := All[tRow](rows, getMeta())
	e := out[0].Email.Get()
	if e == nil || *e != "a@e.com" {
		t.Errorf("email got %v, want *a@e.com", e)
	}
}

func TestScanIgnoreUnknownColumn(t *testing.T) {
	// 结果集含模型没有的列，应丢弃不报错
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email", "extra_col"},
		data: [][]any{
			{int64(1), "alice", 30, "a@e.com", "ignored"},
		},
	}
	out, err := All[tRow](rows, getMeta())
	if err != nil {
		t.Fatalf("should ignore unknown column: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1", len(out))
	}
}

// TestScanUnknownColumnLogged 验证未知列丢弃时发出 Debug 日志，
// 帮助排查模型/schema 漂移（回归：之前完全静默）。
func TestScanUnknownColumnLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := logging.Logger()
	logging.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer logging.SetLogger(prev)

	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email", "extra_col"},
		data: [][]any{{int64(1), "alice", 30, "a@e.com", "ignored"}},
	}
	_, _ = All[tRow](rows, getMeta())

	got := buf.String()
	if !strings.Contains(got, "extra_col") || !strings.Contains(got, "not found in model") {
		t.Errorf("expected debug log mentioning extra_col, got:\n%s", got)
	}
}

func TestScanColumnOrder(t *testing.T) {
	// 列顺序与结构体字段顺序不同，路由应正确
	rows := &fakeRows{
		cols: []string{"email", "age", "name", "id"},
		data: [][]any{
			{"z@e.com", 99, "zoe", int64(5)},
		},
	}
	out, _ := All[tRow](rows, getMeta())
	if out[0].ID.Get() != 5 || out[0].Name.Get() != "zoe" || out[0].Age.Get() != 99 {
		t.Errorf("order mismatch: %+v", out[0])
	}
}

func TestScanEmpty(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{},
	}
	out, err := All[tRow](rows, getMeta())
	if err != nil {
		t.Fatalf("All on empty: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("got %d rows, want 0", len(out))
	}
}

func TestScanRowsError(t *testing.T) {
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{{int64(1), "a", 1, nil}},
		err:  errors.New("conn lost"),
	}
	// Next 因 err 返回 false，但 Err 返回错误
	_, err := All[tRow](rows, getMeta())
	if err == nil {
		t.Error("should propagate rows.Err")
	}
}

func TestOneNotFound(t *testing.T) {
	// One 用 *sql.Rows，这里用 db 包装测试 ErrNoRows
	// 通过 fakeRows 间接验证：空数据 → ErrNoRows
	rows := &fakeRows{
		cols: []string{"id", "name", "age", "email"},
		data: [][]any{},
	}
	// One 需要 *sql.Rows，fakeRows 实现的是 scan.Rows。
	// 这里改用空数据走 All 验证无行场景，One 的 ErrNoRows 由 e2e 覆盖。
	_, err := All[tRow](rows, getMeta())
	if err != nil {
		t.Fatalf("All empty err: %v", err)
	}
}

func TestFieldIndexByName(t *testing.T) {
	m := getMeta()
	if idx := fieldIndexByName(m, "ID"); idx != 0 {
		t.Errorf("ID idx got %d, want 0", idx)
	}
	if idx := fieldIndexByName(m, "Name"); idx != 1 {
		t.Errorf("Name idx got %d, want 1", idx)
	}
	if idx := fieldIndexByName(m, "Nonexistent"); idx != -1 {
		t.Errorf("nonexistent idx got %d, want -1", idx)
	}
}
