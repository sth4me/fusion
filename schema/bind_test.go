package schema

import (
	"testing"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/meta"

	_ "modernc.org/sqlite"
)

// bindModel 用内联结构体注册模型，返回 meta.TableOf 供 Bind 测试。
func bindModel[T any](t *testing.T, name string) meta.TableOf {
	t.Helper()
	return meta.Register[T](name)
}

// TestBindConsistent 模型与 schema 完全一致，无 Diff。
func TestBindConsistent(t *testing.T) {
	cat := &Catalog{Dialect: dialect.SQLiteDialect, tables: map[string]*Table{
		"users": {
			Name: "users",
			Columns: []Column{
				{Name: "id", SQLType: "INTEGER", Nullable: false},
				{Name: "name", SQLType: "TEXT", Nullable: false},
			},
			PrimaryKey: []string{"id"},
		},
	}}

	tab := bindUserModel(t) // id+name，pk=id
	diffs := Bind(cat, tab)
	if len(diffs) != 0 {
		t.Errorf("expected no diffs, got %d: %+v", len(diffs), diffs)
	}
}

// TestBindModelExtraColumn 模型多一列。
func TestBindModelExtraColumn(t *testing.T) {
	cat := &Catalog{Dialect: dialect.SQLiteDialect, tables: map[string]*Table{
		"users": {
			Name: "users",
			Columns: []Column{
				{Name: "id", SQLType: "INTEGER"},
			},
			PrimaryKey: []string{"id"},
		},
	}}
	tab := bindUserModel(t) // id + name
	diffs := Bind(cat, tab)
	found := false
	for _, d := range diffs {
		if d.Kind == DiffModelExtraColumn && d.Column == "name" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DiffModelExtraColumn for name, got %+v", diffs)
	}
}

// TestBindMissingColumn 数据库多一列。
func TestBindMissingColumn(t *testing.T) {
	cat := &Catalog{Dialect: dialect.SQLiteDialect, tables: map[string]*Table{
		"users": {
			Name: "users",
			Columns: []Column{
				{Name: "id", SQLType: "INTEGER"},
				{Name: "name", SQLType: "TEXT"},
				{Name: "email", SQLType: "TEXT"},
			},
			PrimaryKey: []string{"id"},
		},
	}}
	tab := bindUserModel(t) // 只有 id+name，无 email
	diffs := Bind(cat, tab)
	found := false
	for _, d := range diffs {
		if d.Kind == DiffMissingColumn && d.Column == "email" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DiffMissingColumn for email, got %+v", diffs)
	}
}

// TestBindPKMismatch 主键不一致。
func TestBindPKMismatch(t *testing.T) {
	cat := &Catalog{Dialect: dialect.SQLiteDialect, tables: map[string]*Table{
		"users": {
			Name: "users",
			Columns: []Column{
				{Name: "id", SQLType: "INTEGER"},
				{Name: "name", SQLType: "TEXT"},
			},
			PrimaryKey: []string{"name"}, // DB 主键是 name
		},
	}}
	tab := bindUserModel(t) // 模型主键 id
	diffs := Bind(cat, tab)
	found := false
	for _, d := range diffs {
		if d.Kind == DiffPKMismatch {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DiffPKMismatch, got %+v", diffs)
	}
}

// TestBindTableMissing 表未内省。
func TestBindTableMissing(t *testing.T) {
	cat := &Catalog{Dialect: dialect.SQLiteDialect, tables: map[string]*Table{}}
	tab := bindUserModel(t)
	diffs := Bind(cat, tab)
	if len(diffs) != 1 || diffs[0].Kind != "table_missing" {
		t.Errorf("expected one table_missing diff, got %+v", diffs)
	}
}

// --- 测试用真 Col 模型（避免在 schema 包导入 col 造成潜在循环） ---
// 注意：schema 包目前不导入 col；这里用反射构造一个最小 meta。
// 但 meta.Register 需要 Col 字段实现 FieldDescriptor。为避免依赖 col，
// 这里直接用一个独立定义的 descriptor 类型注册。

// testCol 是测试用的字段描述符（实现 meta.FieldDescriptor）。
type testCol struct {
	colName string
	tableN  string
	isPK    bool
}

func (c *testCol) SetMeta(m meta.FieldMeta) {
	c.colName = m.Column
	c.tableN = m.Table
	c.isPK = m.IsPrimaryKey
}
func (c testCol) ColName() string      { return c.colName }
func (c testCol) IsSet() bool           { return false }
func (c testCol) SQLValue() (any, error) { return nil, nil }

type bindUser struct {
	ID   testCol
	Name testCol
}

func bindUserModel(t *testing.T) meta.TableOf {
	t.Helper()
	// meta.Register 会反射遍历字段；testCol 实现 FieldDescriptor 即可。
	// bindUser.ID 无 db tag → 默认首个非关联字段为主键（即 ID），符合预期。
	return meta.Register[bindUser]("users")
}
