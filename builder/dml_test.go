package builder

import (
	"testing"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/meta"
)

type dmlModel struct {
	ID   int64
	Name string
	Age  int
}

func dmlMeta() *meta.ModelMeta {
	return meta.Register[dmlModel]("users").Meta
}

func TestBuildINSERTBasic(t *testing.T) {
	m := dmlMeta()
	sqlStr, args := BuildINSERT(m, InsertQuery{
		Cols:          []string{"name", "age"},
		ReturningCols: []string{"id"},
	}, []any{"alice", 30}, dialect.PostgresDialect)

	want := `INSERT INTO "users" ("name", "age") VALUES ($1, $2) RETURNING "id"`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 2 || args[0] != "alice" || args[1] != 30 {
		t.Errorf("args got %v", args)
	}
}

func TestBuildINSERTMySQLNoReturning(t *testing.T) {
	m := dmlMeta()
	sqlStr, _ := BuildINSERT(m, InsertQuery{
		Cols:          []string{"name", "age"},
		ReturningCols: []string{"id"},
	}, []any{"alice", 30}, dialect.MySQLDialect)

	want := "INSERT INTO `users` (`name`, `age`) VALUES (?, ?)"
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
}

func TestBuildINSERTUpsert(t *testing.T) {
	m := dmlMeta()
	sqlStr, _ := BuildINSERT(m, InsertQuery{
		Cols:         []string{"id", "name", "age"},
		DoUpsert:     true,
		ConflictCols: []string{"id"},
		UpdateCols:   []string{"name", "age"},
	}, []any{1, "alice", 30}, dialect.PostgresDialect)

	want := `INSERT INTO "users" ("id", "name", "age") VALUES ($1, $2, $3) ON CONFLICT ("id") DO UPDATE SET "name" = excluded."name", "age" = excluded."age"`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
}

// TestBuildINSERTUpsertSet 验证 OnConflictSet 自定义表达式渲染（累加场景）。
func TestBuildINSERTUpsertSet(t *testing.T) {
	m := dmlMeta()
	sqlStr, args := BuildINSERT(m, InsertQuery{
		Cols:         []string{"id", "name", "age"},
		DoUpsert:     true,
		ConflictCols: []string{"id"},
		ConflictSets: []UpsertSet{
			{Col: "age", Value: expr.Add(expr.Column("users", "age"), expr.Excluded("age"))},
		},
	}, []any{1, "alice", 30}, dialect.PostgresDialect)

	want := `INSERT INTO "users" ("id", "name", "age") VALUES ($1, $2, $3) ON CONFLICT ("id") DO UPDATE SET "age" = ("age" + excluded."age")`
	if sqlStr != want {
		t.Errorf("PG: got %q, want %q", sqlStr, want)
	}
	if len(args) != 3 {
		t.Errorf("args count = %d, want 3（无额外参数）", len(args))
	}

	// MySQL 路径
	sqlMy, _ := BuildINSERT(m, InsertQuery{
		Cols:         []string{"id", "name", "age"},
		DoUpsert:     true,
		ConflictCols: []string{"id"},
		ConflictSets: []UpsertSet{
			{Col: "age", Value: expr.Add(expr.Column("users", "age"), expr.Excluded("age"))},
		},
	}, []any{1, "alice", 30}, dialect.MySQLDialect)

	wantMy := "INSERT INTO `users` (`id`, `name`, `age`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `age` = (`age` + VALUES(`age`))"
	if sqlMy != wantMy {
		t.Errorf("MySQL: got %q, want %q", sqlMy, wantMy)
	}
}

func TestBuildUPDATEBasic(t *testing.T) {
	m := dmlMeta()
	sqlStr, args := BuildUPDATE(m, UpdateQuery{
		SetCols: []string{"name", "age"},
		Where:   expr.LeafParam("id", "=", int64(1)),
	}, []any{"bob", 20}, dialect.PostgresDialect)

	want := `UPDATE "users" SET "name" = $1, "age" = $2 WHERE "id" = $3`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 3 {
		t.Errorf("args got %v", args)
	}
}

func TestBuildDELETEBasic(t *testing.T) {
	m := dmlMeta()
	sqlStr, args := BuildDELETE(m, DeleteQuery{
		Where: expr.LeafParam("id", "=", int64(1)),
	}, dialect.PostgresDialect)

	want := `DELETE FROM "users" WHERE "id" = $1`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 1 || args[0] != int64(1) {
		t.Errorf("args got %v", args)
	}
}

func TestBuildDELETEMySQL(t *testing.T) {
	m := dmlMeta()
	sqlStr, _ := BuildDELETE(m, DeleteQuery{
		Where: expr.LeafParam("age", "<", 18),
	}, dialect.MySQLDialect)

	want := "DELETE FROM `users` WHERE `age` < ?"
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
}

func TestBuildUPDATEWhereComplex(t *testing.T) {
	m := dmlMeta()
	a := expr.LeafParam("name", "=", "alice")
	b := expr.LeafParam("age", ">", 18)
	sqlStr, args := BuildUPDATE(m, UpdateQuery{
		SetCols: []string{"age"},
		Where:   a.And(b).Or(expr.LeafParam("id", "=", int64(5))),
	}, []any{25}, dialect.PostgresDialect)

	want := `UPDATE "users" SET "age" = $1 WHERE ("name" = $2 AND "age" > $3) OR "id" = $4`
	if sqlStr != want {
		t.Errorf("got %q, want %q", sqlStr, want)
	}
	if len(args) != 4 {
		t.Errorf("args got %v", args)
	}
}
