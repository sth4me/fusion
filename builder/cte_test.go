package builder

import (
	"strings"
	"testing"

	"fusion/dialect"
	"fusion/meta"
)

// cteTestModel 仅用于 BuildSELECT 的 ModelMeta 占位。
type cteTestModel struct {
	ID   int64
	Name string
}

// TestCTE_Render 验证 WITH 子句渲染 + 占位符重写。
func TestCTE_Render(t *testing.T) {
	m := &meta.ModelMeta{Table: "users"}
	m.Fields = []meta.FieldMeta{{FieldName: "ID", Column: "id", Table: "users"}}

	q := SelectQuery{
		Table:      "users",
		SelectCols: nil, // 整表列
		CTEs: []CTESpec{
			{Name: "active", SQL: "SELECT id FROM users WHERE active = ?", Args: []any{true}},
		},
	}
	sqlStr, args := BuildSELECT(m, q, dialect.PostgresDialect)
	// 应含 WITH "active" AS (SELECT id FROM users WHERE active = $1)
	if !strings.HasPrefix(sqlStr, `WITH "active" AS (`) {
		t.Errorf("missing WITH prefix:\n%s", sqlStr)
	}
	if !strings.Contains(sqlStr, "$1") {
		t.Errorf("placeholder not rewritten to $1:\n%s", sqlStr)
	}
	// 主查询 SELECT 应跟在 CTE 后
	if !strings.Contains(sqlStr, "SELECT ") {
		t.Errorf("missing main SELECT:\n%s", sqlStr)
	}
	if len(args) != 1 || args[0] != true {
		t.Errorf("args got %v, want [true]", args)
	}
}

// TestCTE_Recursive 验证 WITH RECURSIVE。
func TestCTE_Recursive(t *testing.T) {
	m := &meta.ModelMeta{Table: "t"}
	q := SelectQuery{
		Table: "t",
		CTEs: []CTESpec{
			{Name: "tree", Recursive: true, Columns: []string{"id", "pid"},
				SQL: "SELECT id, pid FROM t WHERE id = ? UNION ALL SELECT c.id, c.pid FROM t c JOIN tree ON c.pid = tree.id",
				Args: []any{1}},
		},
	}
	sqlStr, args := BuildSELECT(m, q, dialect.PostgresDialect)
	if !strings.HasPrefix(sqlStr, "WITH RECURSIVE ") {
		t.Errorf("missing WITH RECURSIVE:\n%s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"tree" ("id", "pid") AS (`) {
		t.Errorf("missing CTE columns list:\n%s", sqlStr)
	}
	if len(args) != 1 || args[0] != 1 {
		t.Errorf("args got %v", args)
	}
}

// TestCTE_MultipleAndPlaceholderOrdering 多个 CTE + 占位符顺序连续。
func TestCTE_MultipleAndPlaceholderOrdering(t *testing.T) {
	m := &meta.ModelMeta{Table: "t"}
	q := SelectQuery{
		Table: "t",
		CTEs: []CTESpec{
			{Name: "a", SQL: "SELECT id FROM t WHERE x = ?", Args: []any{1}},
			{Name: "b", SQL: "SELECT id FROM t WHERE y = ?", Args: []any{2}},
		},
	}
	sqlStr, args := BuildSELECT(m, q, dialect.PostgresDialect)
	// 两个 CTE 参数应连续编号 $1, $2
	if !strings.Contains(sqlStr, "$1") || !strings.Contains(sqlStr, "$2") {
		t.Errorf("placeholders should be $1, $2:\n%s", sqlStr)
	}
	if len(args) != 2 || args[0] != 1 || args[1] != 2 {
		t.Errorf("args got %v, want [1 2]", args)
	}
}
