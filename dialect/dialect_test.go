package dialect

import "testing"

func TestPostgresPlaceholder(t *testing.T) {
	d := PostgresDialect
	if got := d.Placeholder(1); got != "$1" {
		t.Errorf("got %q, want $1", got)
	}
	if got := d.Placeholder(5); got != "$5" {
		t.Errorf("got %q, want $5", got)
	}
}

func TestMySQLSQLitePlaceholder(t *testing.T) {
	if got := MySQLDialect.Placeholder(1); got != "?" {
		t.Errorf("mysql got %q, want ?", got)
	}
	if got := SQLiteDialect.Placeholder(1); got != "?" {
		t.Errorf("sqlite got %q, want ?", got)
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := PostgresDialect.QuoteIdent("order"); got != `"order"` {
		t.Errorf("pg got %q", got)
	}
	if got := MySQLDialect.QuoteIdent("order"); got != "`order`" {
		t.Errorf("mysql got %q", got)
	}
	if got := SQLiteDialect.QuoteIdent("order"); got != `"order"` {
		t.Errorf("sqlite got %q", got)
	}
}

func TestQuoteIdentEscape(t *testing.T) {
	// 含双引号的列名需转义
	if got := PostgresDialect.QuoteIdent(`a"b`); got != `"a""b"` {
		t.Errorf("pg escape got %q", got)
	}
}

func TestQuoteTableSchema(t *testing.T) {
	got := PostgresDialect.QuoteTable("public.users")
	want := `"public"."users"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSupportsReturning(t *testing.T) {
	if !PostgresDialect.SupportsReturning() {
		t.Error("pg should support returning")
	}
	if !SQLiteDialect.SupportsReturning() {
		t.Error("sqlite should support returning")
	}
	if MySQLDialect.SupportsReturning() {
		t.Error("default mysql (legacy) should not support returning")
	}
	m := &MySQL{SupportsRet: true}
	if !m.SupportsReturning() {
		t.Error("mysql 8.0+ should support returning when configured")
	}
}

// TestUpsertDiff 验证三方言 UPSERT 语法差异（#4 核心）
func TestUpsertDiff(t *testing.T) {
	conflict := []string{`"id"`}
	update := []string{`"name"`, `"age"`}

	pg := PostgresDialect.UpsertOnConflict(conflict, update)
	wantPG := ` ON CONFLICT ("id") DO UPDATE SET "name" = excluded."name", "age" = excluded."age"`
	if pg != wantPG {
		t.Errorf("pg got %q, want %q", pg, wantPG)
	}

	my := MySQLDialect.UpsertOnConflict(conflict, update)
	// MySQL 忽略 conflictCols
	wantMy := ` ON DUPLICATE KEY UPDATE "name" = VALUES("name"), "age" = VALUES("age")`
	if my != wantMy {
		t.Errorf("mysql got %q, want %q", my, wantMy)
	}

	sl := SQLiteDialect.UpsertOnConflict(conflict, update)
	if sl != wantPG {
		t.Errorf("sqlite should match pg syntax, got %q", sl)
	}
}
