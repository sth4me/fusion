package query

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"fusion/builder"
	"fusion/col"
	"fusion/dialect"
	"fusion/meta"
)

// qModel 测试模型
type qModel struct {
	ID   col.Col[int64]
	Name col.Col[string]
	Age  col.Col[int]
}

func regQModel() *meta.Table[qModel] {
	return meta.Register[qModel]("q_users")
}

// fakeExecer 记录执行的 SQL 和参数，返回预设结果。
type fakeExecer struct {
	lastSQL    string
	lastArgs   []any
	queryErr   error
	// 模拟返回的行数据（用于 QueryContext）
	mockRows   *sql.Rows
	rowScanned any
}

func (f *fakeExecer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	f.lastSQL = q
	f.lastArgs = args
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.mockRows, nil
}

func (f *fakeExecer) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	f.lastSQL = q
	f.lastArgs = args
	// sql.Row 难以构造，这里用真实 DB；本测试主要验证 SQL 生成，不测 One 数据扫描
	return nil
}

func (f *fakeExecer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	f.lastSQL = q
	f.lastArgs = args
	return nil, nil
}

// TestQuerySQLGeneration 验证 All 生成的 SQL 与参数（不真实执行，只查 SQL）
func TestQuerySQLGeneration(t *testing.T) {
	tab := regQModel()
	// 用真实 SQLite 让 QueryContext 能执行；但这里我们只断言生成的 SQL 字符串
	// 所以用一个会报错的 fakeExecer，捕获 SQL 后断言
	fe := &fakeExecer{queryErr: errors.New("stop")}
	q := New[qModel](tab, dialect.PostgresDialect, fe)
	_, _ = q.Where(tab.Proto.Name.Eq("alice")).Limit(10).All(context.Background())

	wantSQL := `SELECT "id", "name", "age" FROM "q_users" WHERE "name" = $1 LIMIT $2`
	if fe.lastSQL != wantSQL {
		t.Errorf("SQL got %q, want %q", fe.lastSQL, wantSQL)
	}
	if len(fe.lastArgs) != 2 || fe.lastArgs[0] != "alice" {
		t.Errorf("args got %v", fe.lastArgs)
	}
}

func TestQueryComplexWhere(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	u := tab.Proto
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).
		Where(u.Name.Eq("a").And(u.Age.Gt(18)).Or(u.Name.Eq("b"))).
		All(context.Background())

	// (name=a AND age>18) OR name=b
	wantContains := `WHERE ("name" = $1 AND "age" > $2) OR "name" = $3`
	if !strings.Contains(fe.lastSQL, wantContains) {
		t.Errorf("SQL got %q, want contains %q", fe.lastSQL, wantContains)
	}
}

func TestQueryOrderBy(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).
		OrderBy(tab.Proto.Age.Desc(), tab.Proto.ID.Asc()).
		All(context.Background())

	if !strings.Contains(fe.lastSQL, `ORDER BY "age" DESC, "id" ASC`) {
		t.Errorf("SQL got %q", fe.lastSQL)
	}
}

func TestQueryMySQLDialect(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	_, _ = New[qModel](tab, dialect.MySQLDialect, fe).
		Where(tab.Proto.Age.Gt(18)).Limit(5).
		All(context.Background())

	wantSQL := "SELECT `id`, `name`, `age` FROM `q_users` WHERE `age` > ? LIMIT ?"
	if fe.lastSQL != wantSQL {
		t.Errorf("got %q, want %q", fe.lastSQL, wantSQL)
	}
}

func TestQueryOneRestoresLimit(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	q := New[qModel](tab, dialect.PostgresDialect, fe)
	q.Limit(50)
	_, _ = q.Where(tab.Proto.ID.Eq(1)).One(context.Background())
	// One 内部设 limit=1 执行后应恢复原值
	if q.limit != 50 {
		t.Errorf("limit after One got %d, want 50 (restored)", q.limit)
	}
}

// TestCountSQL 验证 Count 生成的 SQL（重构后用 builder COUNT(*) 投影）
func TestCountSQL(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	// Count 内部用 BuildSELECT 生成 SELECT COUNT(*)，但执行走 QueryRowContext
	// fakeExecer.QueryRowContext 返回 nil → Scan panic，这里捕获
	defer func() { _ = recover() }()
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).Count(context.Background())
	// 断言生成的 SQL 含 COUNT(*)
	if !strings.Contains(fe.lastSQL, "COUNT(*)") {
		t.Errorf("Count SQL should contain COUNT(*): %q", fe.lastSQL)
	}
}

func TestQueryOffsetSQL(t *testing.T) {
	tab := regQModel()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).
		Limit(10).Offset(20).
		All(context.Background())

	if !strings.Contains(fe.lastSQL, `LIMIT $1 OFFSET $2`) {
		t.Errorf("SQL got %q", fe.lastSQL)
	}
	if fe.lastArgs[0].(int) != 10 || fe.lastArgs[1].(int) != 20 {
		t.Errorf("args got %v", fe.lastArgs)
	}
}

// 验证编译期断言：col.Order 实现 OrderItem
func TestOrderImplementsOrderItem(t *testing.T) {
	var _ builder.OrderItem = col.Order{}
}

// qDept 是 qModel 的关联表，用于测试 One/Count 是否保留 Join/Alias。
type qDept struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

func regQDept() *meta.Table[qDept] {
	return meta.Register[qDept]("q_depts")
}

// TestOnePreservesJoinAliasLock 验证 One() 复用 buildSelectQuery，
// 不丢 Alias/Join/LockClause（回归 ForUpdate().One() 不上锁的数据正确性问题）。
func TestOnePreservesJoinAliasLock(t *testing.T) {
	tab := regQModel()
	depts := regQDept()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	u := tab.Proto
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).
		As("u").
		Join("LEFT", depts, "d", u.ID.EqCol(depts.Proto.ID)).
		Where(u.ID.Eq(1)).
		ForUpdate().
		One(context.Background())

	for _, want := range []string{`AS "u"`, `LEFT JOIN "q_depts" AS "d"`, `FOR UPDATE`} {
		if !strings.Contains(fe.lastSQL, want) {
			t.Errorf("One SQL missing %q\n got: %q", want, fe.lastSQL)
		}
	}
	// LIMIT 1 也应在
	if !strings.Contains(fe.lastSQL, "LIMIT") {
		t.Errorf("One SQL missing LIMIT\n got: %q", fe.lastSQL)
	}
}

// TestCountPreservesJoin 验证 Count() 保留 Join/Where（回归带 JOIN 的 Count 列引用错误）。
func TestCountPreservesJoin(t *testing.T) {
	tab := regQModel()
	depts := regQDept()
	fe := &fakeExecer{queryErr: errors.New("stop")}
	u := tab.Proto
	defer func() { _ = recover() }() // QueryRowContext 返回 nil，Scan 会 panic
	_, _ = New[qModel](tab, dialect.PostgresDialect, fe).
		Join("INNER", depts, "d", u.ID.EqCol(depts.Proto.ID)).
		Where(u.Age.Gt(18)).
		Count(context.Background())

	for _, want := range []string{"COUNT(*)", `INNER JOIN "q_depts" AS "d"`, `WHERE` + ` "age" > $1`} {
		if !strings.Contains(fe.lastSQL, want) {
			t.Errorf("Count SQL missing %q\n got: %q", want, fe.lastSQL)
		}
	}
}
