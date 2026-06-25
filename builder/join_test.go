package builder

import (
	"strings"
	"testing"

	"fusion/col"
	"fusion/dialect"
	"fusion/expr"
	"fusion/meta"
)

type jUser struct {
	ID     col.Col[int64]
	Name   col.Col[string]
	DeptID col.Col[int64]
}
type jDept struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

func jUserTab() *meta.Table[jUser] { return meta.Register[jUser]("jusers") }
func jDeptTab() *meta.Table[jDept] { return meta.Register[jDept]("jdepts") }

func TestBuildJoin(t *testing.T) {
	Users := jUserTab()
	Depts := jDeptTab()
	// 模拟 query 层给 Proto 设表别名
	Users.Proto.ID.SetTableAlias("t0")
	Users.Proto.DeptID.SetTableAlias("t0")
	Depts.Proto.ID.SetTableAlias("t1")
	u := Users.Proto
	d := Depts.Proto

	sqlStr, args := BuildSELECT(Users.Meta, SelectQuery{
		Alias: "t0",
		Joins: []JoinSpec{
			{Kind: "INNER", Table: "jdepts", Alias: "t1", On: u.DeptID.EqCol(d.ID)},
		},
		Where: u.ID.Gt(100),
	}, dialect.PostgresDialect)

	if !strings.Contains(sqlStr, `FROM "jusers" AS "t0"`) {
		t.Errorf("missing FROM alias: %q", sqlStr)
	}
	if !strings.Contains(sqlStr, `INNER JOIN "jdepts" AS "t1"`) {
		t.Errorf("missing JOIN: %q", sqlStr)
	}
	if !strings.Contains(sqlStr, `ON "t0"."dept_id" = "t1"."id"`) {
		t.Errorf("missing ON: %q", sqlStr)
	}
	if len(args) != 1 {
		t.Errorf("args got %v", args)
	}
}

func TestBuildSelectProjection(t *testing.T) {
	Users := jUserTab()
	Depts := jDeptTab()
	u := Users.Proto
	d := Depts.Proto

	sqlStr, _ := BuildSELECT(Users.Meta, SelectQuery{
		SelectCols: []SelectItem{
			u.Name.As("user_name"),
			d.Name.As("dept_name"),
		},
		Joins: []JoinSpec{
			{Kind: "INNER", Table: "jdepts", Alias: "t1", On: u.DeptID.EqCol(d.ID)},
		},
	}, dialect.PostgresDialect)

	if !strings.Contains(sqlStr, `SELECT "name" AS user_name, "name" AS dept_name`) {
		t.Errorf("projection got %q", sqlStr)
	}
}

func TestBuildGroupByHaving(t *testing.T) {
	Users := jUserTab()
	u := Users.Proto

	sqlStr, args := BuildSELECT(Users.Meta, SelectQuery{
		SelectCols: []SelectItem{
			col.Count[int64]().As("cnt"),
			u.DeptID.As("dept_id"),
		},
		GroupBy: []GroupItem{u.DeptID.GroupBy()},
		Having:  expr.LeafRaw("cnt", "> 1"), // 简化 HAVING
	}, dialect.PostgresDialect)

	if !strings.Contains(sqlStr, "GROUP BY") {
		t.Errorf("missing GROUP BY: %q", sqlStr)
	}
	if !strings.Contains(sqlStr, "HAVING") {
		t.Errorf("missing HAVING: %q", sqlStr)
	}
	_ = args
}

func TestBuildDistinct(t *testing.T) {
	Users := jUserTab()
	u := Users.Proto

	sqlStr, _ := BuildSELECT(Users.Meta, SelectQuery{
		SelectCols: []SelectItem{u.DeptID.As("dept_id")},
		Distinct:   true,
	}, dialect.PostgresDialect)

	if !strings.HasPrefix(sqlStr, "SELECT DISTINCT") {
		t.Errorf("missing DISTINCT: %q", sqlStr)
	}
}

func TestBuildLeftJoin(t *testing.T) {
	Users := jUserTab()
	Depts := jDeptTab()
	u := Users.Proto
	d := Depts.Proto

	sqlStr, _ := BuildSELECT(Users.Meta, SelectQuery{
		Joins: []JoinSpec{
			{Kind: "LEFT", Table: "jdepts", On: u.DeptID.EqCol(d.ID)},
		},
	}, dialect.PostgresDialect)

	if !strings.Contains(sqlStr, "LEFT JOIN") {
		t.Errorf("missing LEFT JOIN: %q", sqlStr)
	}
}

// 验证 col.SelectItem 实现 builder.SelectItem 接口
func TestSelectItemImplementsBuilder(t *testing.T) {
	var _ SelectItem = col.SelectItem{}
}

// 验证 col.Order 实现 GroupItem（Asc/Desc 用于 GROUP BY 不常见，但接口兼容）
func TestOrderImplementsGroupItem(t *testing.T) {
	var _ GroupItem = col.Order{}
}
