package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/rel"
)

// Join/聚合测试模型
type JUser struct {
	ID     col.Col[int64]
	Name   col.Col[string]
	DeptID col.Col[int64]
}
type JDept struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

type UserDeptView struct {
	UserName string  `db:"user_name"`
	DeptName *string `db:"dept_name"` // LEFT JOIN 可能 NULL，用 *string
}

type DeptStat struct {
	DeptID int64 `db:"dept_id"`
	Cnt    int64 `db:"cnt"`
}

func setupJoinDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db := openSQLite(t)
	mustExecP(db, `CREATE TABLE jusers (id INTEGER PRIMARY KEY, name TEXT, dept_id INTEGER)`)
	mustExecP(db, `CREATE TABLE jdepts (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExecP(db, `INSERT INTO jdepts VALUES (1,'工程部'),(2,'市场部')`)
	mustExecP(db, `INSERT INTO jusers VALUES (1,'alice',1),(2,'bob',1),(3,'carol',2),(4,'dave',1)`)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	fusion.Register[JDept]("jdepts")
	fusion.Register[JUser]("jusers")
	fusion.Register[UserDeptView]("")
	fusion.Register[DeptStat]("")
	return fusion.WrapDB(db), db
}

// TestJoin_Projection 灵活 Join 投影到自定义结构体。
// 用法：As 设主表别名 → Join 设连接表别名 + 回调构造 ON（此时双表别名已就绪）。
func TestJoin_Projection(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	Users := fusion.Register[JUser]("jusers")
	Depts := fusion.Register[JDept]("jdepts")

	var view []UserDeptView
	q := fusion.From(Users, wrapped).As("u")
	q.Join(fusion.InnerJoin, Depts, "d", Users.Proto.DeptID.EqCol(Depts.Proto.ID))
	q.Select(Users.Proto.Name.As("user_name"), Depts.Proto.Name.As("dept_name"))

	if err := q.AllInto(context.Background(), &view); err != nil {
		t.Fatalf("join: %v", err)
	}
	if len(view) != 4 {
		t.Fatalf("got %d rows, want 4", len(view))
	}
	wantAlice := false
	for _, v := range view {
		if v.UserName == "alice" && v.DeptName != nil && *v.DeptName == "工程部" {
			wantAlice = true
		}
	}
	if !wantAlice {
		t.Errorf("alice/工程部 not found in %v", view)
	}
}

// TestAggregate_GroupByCount GROUP BY + COUNT
func TestAggregate_GroupByCount(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	Users := fusion.Register[JUser]("jusers")

	var stats []DeptStat
	q := fusion.From(Users, wrapped).As("u")
	q.Select(Users.Proto.DeptID.As("dept_id"), fusion.Count[int64]().As("cnt"))
	q.GroupBy(Users.Proto.DeptID.GroupBy())

	if err := q.AllInto(context.Background(), &stats); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	byDept := map[int64]int64{}
	for _, s := range stats {
		byDept[s.DeptID] = s.Cnt
	}
	if byDept[1] != 3 {
		t.Errorf("dept 1 count got %d, want 3", byDept[1])
	}
	if byDept[2] != 1 {
		t.Errorf("dept 2 count got %d, want 1", byDept[2])
	}
}

// TestDistinct DISTINCT 去重
func TestDistinct(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	Users := fusion.Register[JUser]("jusers")

	type DistinctDept struct {
		DeptID int64 `db:"dept_id"`
	}
	fusion.Register[DistinctDept]("")

	var depts []DistinctDept
	q := fusion.From(Users, wrapped).As("u").Distinct()
	q.Select(Users.Proto.DeptID.As("dept_id"))

	if err := q.AllInto(context.Background(), &depts); err != nil {
		t.Fatalf("distinct: %v", err)
	}
	if len(depts) != 2 {
		t.Errorf("got %d distinct depts, want 2", len(depts))
	}
}

// TestLeftJoin LEFT JOIN（保留无关联行）
func TestLeftJoin(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	mustExecP(raw, `INSERT INTO jusers VALUES (5,'eve',999)`)

	Users := fusion.Register[JUser]("jusers")
	Depts := fusion.Register[JDept]("jdepts")

	var view []UserDeptView
	q := fusion.From(Users, wrapped).As("u")
	q.Join(fusion.LeftJoin, Depts, "d", Users.Proto.DeptID.EqCol(Depts.Proto.ID))
	q.Select(Users.Proto.Name.As("user_name"), Depts.Proto.Name.As("dept_name"))

	if err := q.AllInto(context.Background(), &view); err != nil {
		t.Fatalf("left join: %v", err)
	}
	if len(view) != 5 {
		t.Fatalf("got %d rows, want 5 (including eve)", len(view))
	}
	foundEve := false
	for _, v := range view {
		if v.UserName == "eve" && v.DeptName == nil {
			foundEve = true
		}
	}
	if !foundEve {
		t.Error("eve with nil dept not found")
	}
}

// TestCount 重构后 Count 行为验证
func TestCount(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	Users := fusion.Register[JUser]("jusers")

	// 全部计数
	n, err := fusion.From(Users, wrapped).Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 4 {
		t.Errorf("count all got %d, want 4", n)
	}

	// 带 WHERE 计数（列引用用稳定表名，无需手动清别名）
	n2, err := fusion.From(Users, wrapped).
		Where(Users.Proto.DeptID.Eq(1)).
		Count(context.Background())
	if err != nil {
		t.Fatalf("count where: %v", err)
	}
	if n2 != 3 {
		t.Errorf("count dept=1 got %d, want 3", n2)
	}
}

// TestHaving GROUP BY + HAVING
func TestHaving(t *testing.T) {
	wrapped, raw := setupJoinDB(t)
	defer raw.Close()
	Users := fusion.Register[JUser]("jusers")

	type DeptCnt struct {
		DeptID int64 `db:"dept_id"`
		Cnt    int64 `db:"cnt"`
	}
	fusion.Register[DeptCnt]("")

	// 设别名后构造
	fusion.From(Users, wrapped).As("u")
	var stats []DeptCnt
	q := fusion.From(Users, wrapped).As("u")
	q.Select(Users.Proto.DeptID.As("dept_id"), fusion.Count[int64]().As("cnt"))
	q.GroupBy(Users.Proto.DeptID.GroupBy())
	// HAVING COUNT(*) > 1（只保留工程部 dept_id=1，3人）
	q.Having(col.CountGt(1))

	if err := q.AllInto(context.Background(), &stats); err != nil {
		t.Fatalf("having: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d groups, want 1 (only 工程部 has >1)", len(stats))
	}
	if stats[0].DeptID != 1 || stats[0].Cnt != 3 {
		t.Errorf("got %+v, want dept 1 cnt 3", stats[0])
	}
}

var _ = rel.ErrNotLoaded
