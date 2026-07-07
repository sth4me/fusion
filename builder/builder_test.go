package builder

import (
	"testing"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/meta"
)

type tUser struct {
	ID   int64  // 非包装字段用于纯 meta 测试（builder 不依赖 Col）
	Name string
	Age  int
}

func TestBuildSELECTBasic(t *testing.T) {
	tab := meta.Register[tUser]("users")
	sqlStr, args := BuildSELECT(tab.Meta, SelectQuery{}, dialect.PostgresDialect)
	want := `SELECT "id", "name", "age" FROM "users"`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 0 {
		t.Errorf("args got %v, want none", args)
	}
}

func TestBuildSELECTWhere(t *testing.T) {
	tab := meta.Register[tUser]("users")
	// 模拟一个 WHERE：name = ?
	where := expr.LeafParam("name", "=", "alice")
	sqlStr, args := BuildSELECT(tab.Meta, SelectQuery{Where: where}, dialect.PostgresDialect)
	want := `SELECT "id", "name", "age" FROM "users" WHERE "name" = $1`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args got %v", args)
	}
}

func TestBuildSELECTLimitOffset(t *testing.T) {
	tab := meta.Register[tUser]("users")
	sqlStr, args := BuildSELECT(tab.Meta, SelectQuery{
		Limit:  10,
		Offset: 20,
	}, dialect.PostgresDialect)
	want := `SELECT "id", "name", "age" FROM "users" LIMIT $1 OFFSET $2`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 2 || args[0].(int) != 10 || args[1].(int) != 20 {
		t.Errorf("args got %v", args)
	}
}

func TestBuildSELECTMySQL(t *testing.T) {
	tab := meta.Register[tUser]("users")
	where := expr.LeafParam("age", ">", 18)
	sqlStr, args := BuildSELECT(tab.Meta, SelectQuery{Where: where, Limit: 5}, dialect.MySQLDialect)
	want := "SELECT `id`, `name`, `age` FROM `users` WHERE `age` > ? LIMIT ?"
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 2 {
		t.Errorf("args got %v", args)
	}
}

func TestBuildSELECTAlias(t *testing.T) {
	tab := meta.Register[tUser]("users")
	sqlStr, _ := BuildSELECT(tab.Meta, SelectQuery{Alias: "t0"}, dialect.PostgresDialect)
	want := `SELECT "t0"."id", "t0"."name", "t0"."age" FROM "users" AS "t0"`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
}
