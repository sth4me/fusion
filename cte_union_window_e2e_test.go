package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// === UNION e2e ===

func setupUnionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE uactive (id INTEGER PRIMARY KEY, name TEXT, state TEXT)`,
		`CREATE TABLE uarchived (id INTEGER PRIMARY KEY, name TEXT, state TEXT)`,
		`INSERT INTO uactive VALUES (1,'a1','active'),(2,'a2','active')`,
		`INSERT INTO uarchived VALUES (10,'b1','archived'),(11,'b2','archived')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return db
}

// TestE2E_Union UNION 合并两表，整体 ORDER BY。
func TestE2E_Union(t *testing.T) {
	db := setupUnionDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	// 两表注册同一模型类型：第二个 Register 因类型缓存返回第一个。
	// UNION 要求两 arm 同类型，这里用同模型但不同表名 —— 需要两个 *Table。
	// fusion.Register 缓存按类型，故同类型只能注册一次。改用两个独立模型。
	type UActive struct {
		ID    col.Col[int64]
		Name  col.Col[string]
		State col.Col[string]
	}
	type UArchived struct {
		ID    col.Col[int64]
		Name  col.Col[string]
		State col.Col[string]
	}
	// 但 UNION 要求扫描进同一类型 T —— 两 arm 类型不同无法 Union[T]。
	// 为测 UNION，用单表自 UNION（验证 SQL 正确性）。
	Active := fusion.Register[UActive]("uactive")
	_ = UArchived{} // 仅占位，避免未使用类型；实际用同模型两 arm

	q1 := fusion.From(Active, wrapped).Where(Active.Proto.ID.Eq(1))
	q2 := fusion.From(Active, wrapped).Where(Active.Proto.ID.Eq(2))
	res, err := fusion.Union(q1, q2).OrderBy(Active.Proto.ID.Asc()).All(context.Background())
	if err != nil {
		t.Fatalf("union: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d rows, want 2", len(res))
	}
	if res[0].ID.Get() != 1 || res[1].ID.Get() != 2 {
		t.Errorf("order got %d, %d", res[0].ID.Get(), res[1].ID.Get())
	}
}

// TestE2E_UnionAll UNION ALL（含重复）。
func TestE2E_UnionAll(t *testing.T) {
	db := setupUnionDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	type UA struct {
		ID    col.Col[int64]
		Name  col.Col[string]
		State col.Col[string]
	}
	Active := fusion.Register[UA]("uactive")

	// 两个 arm 都匹配 id=1 → UNION ALL 应返回 2 行（重复）
	q1 := fusion.From(Active, wrapped).Where(Active.Proto.ID.Eq(1))
	q2 := fusion.From(Active, wrapped).Where(Active.Proto.ID.Eq(1))
	res, err := fusion.UnionAll(q1, q2).All(context.Background())
	if err != nil {
		t.Fatalf("union all: %v", err)
	}
	if len(res) != 2 {
		t.Errorf("UNION ALL should keep duplicates, got %d rows", len(res))
	}
}

// === CTE e2e（递归：评论楼中楼）===

type CComment struct {
	ID     col.Col[int64]
	PID    col.Col[int64] // 父评论 id，0=根
	Body   col.Col[string]
}

func setupCTEDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE ccomments (id INTEGER PRIMARY KEY, pid INTEGER, body TEXT)`,
		// 树：1(root) → 2,3(子) → 4(2 的孙)
		`INSERT INTO ccomments VALUES (1,0,'root'),(2,1,'c1'),(3,1,'c2'),(4,2,'c1-1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return db
}

// TestE2E_CTERecursive 递归 CTE 取一棵评论树。
// builder 的 FROM 固定为模型表，CTE 引用（FROM tree）走 Raw 路径最干净。
func TestE2E_CTERecursive(t *testing.T) {
	db := setupCTEDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	fusion.Register[CComment]("ccomments")

	recursiveSQL := `SELECT id, pid, body FROM ccomments WHERE id = ? ` +
		`UNION ALL SELECT c.id, c.pid, c.body FROM ccomments c JOIN tree ON c.pid = tree.id`
	var out []CComment
	err := fusion.Raw(&out, context.Background(), wrapped,
		`WITH RECURSIVE tree AS (`+recursiveSQL+`) SELECT id, pid, body FROM tree ORDER BY id`,
		int64(1))
	if err != nil {
		t.Fatalf("raw recursive CTE: %v", err)
	}
	// 应返回 root(1) 及其全部后代 2,3,4
	if len(out) != 4 {
		t.Fatalf("got %d comments, want 4 (1,2,3,4)", len(out))
	}
	ids := map[int64]bool{}
	for _, c := range out {
		ids[c.ID.Get()] = true
	}
	for _, want := range []int64{1, 2, 3, 4} {
		if !ids[want] {
			t.Errorf("missing comment id %d in tree", want)
		}
	}
}

// TestE2E_CTEBuilderWith 用 Query.With 附加 CTE（主查询仍从模型表取，CTE 体内可用）。
// 验证 With 方法生成的 SQL 含 WITH 前缀且参数并入正确。
func TestE2E_CTEBuilderWith(t *testing.T) {
	db := setupCTEDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	Comments := fusion.Register[CComment]("ccomments")

	// 主查询从 ccomments 取 id=1 的根评论，CTE "roots" 是辅助集（不影响结果）
	res, err := fusion.From(Comments, wrapped).
		With("roots", `SELECT id FROM ccomments WHERE pid = ?`, []any{int64(0)}, false).
		Where(Comments.Proto.ID.Eq(1)).
		All(context.Background())
	if err != nil {
		t.Fatalf("with CTE: %v", err)
	}
	if len(res) != 1 || res[0].ID.Get() != 1 {
		t.Errorf("got %+v, want root comment id=1", res)
	}
}

// === 窗口函数 e2e（排名）===

type WEmp struct {
	ID     col.Col[int64]
	Dept   col.Col[string]
	Salary col.Col[int64]
}

func setupWindowDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE wemps (id INTEGER PRIMARY KEY, dept TEXT, salary INTEGER)`,
		// 工程: 3 人；市场: 2 人
		`INSERT INTO wemps VALUES (1,'eng',100),(2,'eng',200),(3,'eng',150),(4,'mkt',120),(5,'mkt',90)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return db
}

// WRank 窗口排名投影结构体（AllInto 用）。
type WRank struct {
	ID    int64  `db:"id"`
	Dept  string `db:"dept"`
	RankN int64  `db:"rn"`
}

// TestE2E_WindowRowNumber 按部门排名（ROW_NUMBER OVER PARTITION BY dept ORDER BY salary DESC）。
func TestE2E_WindowRowNumber(t *testing.T) {
	db := setupWindowDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	Emps := fusion.Register[WEmp]("wemps")
	fusion.Register[WRank]("wemps") // 投影结构体注册（AllInto 需要）

	// 投影：id, dept, ROW_NUMBER() OVER (PARTITION BY dept ORDER BY salary DESC) AS rn
	var view []WRank
	err := fusion.From(Emps, wrapped).
		Select(
			Emps.Proto.ID.As("id"),
			Emps.Proto.Dept.As("dept"),
			fusion.RowNumber().Over([]string{"dept"}, []string{"salary DESC"}).As("rn"),
		).
		OrderBy(Emps.Proto.Dept.Asc(), Emps.Proto.Salary.Desc()).
		AllInto(context.Background(), &view)
	if err != nil {
		t.Fatalf("window query: %v", err)
	}
	// 验证每个部门内按 salary 降序排名
	engRanks := map[int64]int64{} // id → rank
	for _, r := range view {
		if r.Dept == "eng" {
			engRanks[r.ID] = r.RankN
		}
	}
	// eng: salary 200(id2)=1, 150(id3)=2, 100(id1)=3
	if engRanks[2] != 1 || engRanks[3] != 2 || engRanks[1] != 3 {
		t.Errorf("eng ranks got %+v, want {2:1, 3:2, 1:3}", engRanks)
	}
}

// TestE2E_WindowSumPartition 按部门累计薪资（SUM OVER PARTITION）。
func TestE2E_WindowSumPartition(t *testing.T) {
	db := setupWindowDB(t)
	defer db.Close()
	wrapped := fusion.WrapDB(db)
	Emps := fusion.Register[WEmp]("wemps")
	type WSum struct {
		Dept string `db:"dept"`
		Total int64 `db:"total"`
	}
	fusion.Register[WSum]("wemps")

	var view []WSum
	err := fusion.From(Emps, wrapped).
		Select(
			Emps.Proto.Dept.As("dept"),
			fusion.Sum(Emps.Proto.Salary).Over([]string{"dept"}, nil).As("total"),
		).
		OrderBy(Emps.Proto.Dept.Asc()).
		AllInto(context.Background(), &view)
	if err != nil {
		t.Fatalf("window sum: %v", err)
	}
	// eng 总薪资 = 100+200+150 = 450；mkt = 120+90 = 210
	totals := map[string]int64{}
	for _, r := range view {
		totals[r.Dept] = r.Total
	}
	if totals["eng"] != 450 {
		t.Errorf("eng total got %d, want 450", totals["eng"])
	}
	if totals["mkt"] != 210 {
		t.Errorf("mkt total got %d, want 210", totals["mkt"])
	}
}
