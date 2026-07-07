package fusion_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"

	_ "modernc.org/sqlite"
)

// SUser 用于 schema 内省 + Bind 测试（独立模型避免缓存冲突）。
type SUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Email col.Col[*string]
}

func openSchemaE2EDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE susers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

// TestE2E_LoadSchemaAndBind 一致场景：模型与表完全匹配，Bind 无差异。
func TestE2E_LoadSchemaAndBind(t *testing.T) {
	db := openSchemaE2EDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[SUser]("susers")

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "susers")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	diffs := fusion.BindModel(cat, Users)
	if len(diffs) != 0 {
		t.Errorf("expected no diffs, got %+v", diffs)
	}
}

// TestE2E_BindDetectsDrift 漂移场景：模型少写 email 字段。
func TestE2E_BindDetectsDrift(t *testing.T) {
	db := openSchemaE2EDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)

	// 注册一个只有 id+name 的模型（缺 email）→ 应报 missing_column
	type SUserShort struct {
		ID   col.Col[int64]
		Name col.Col[string]
	}
	UsersShort := fusion.Register[SUserShort]("susers")

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "susers")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	diffs := fusion.BindModel(cat, UsersShort)
	found := false
	for _, d := range diffs {
		if d.Kind == "missing_column" && d.Column == "email" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing_column for email, got %+v", diffs)
	}
}

// TestE2E_MustBindPanicsOnDrift 有差异时 MustBind panic。
func TestE2E_MustBindPanicsOnDrift(t *testing.T) {
	db := openSchemaE2EDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)

	type SUserShort struct {
		ID   col.Col[int64]
		Name col.Col[string]
	}
	UsersShort := fusion.Register[SUserShort]("susers")

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "susers")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustBind should panic on drift")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "drift") || !strings.Contains(msg, "email") {
			t.Errorf("panic message should mention drift/email, got %q", msg)
		}
	}()
	fusion.MustBind(cat, UsersShort)
}
