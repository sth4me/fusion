// Package schema 提供数据库 schema 的运行时内省（introspection）。
//
// 与 meta 包（从 Go 结构体反射）并列互补：
//   - meta.ModelMeta：模型知道的事（字段名、列名、主键）——来自反射
//   - schema.Table ：数据库知道的事（列类型、可空、外键、索引）——来自 information_schema/PRAGMA
//
// 用途：
//  1. Bind：校验已注册模型与数据库 schema 是否一致（启动期抓漂移，见 Bind）。
//  2. AutoRegisterRelations：从外键自动注册 belongs_to/has_many（手动优先）。
//  3. 缓存 schema 元信息供查询/错误诊断用。
//
// 不生成 .go 源码（运行时元数据路线，符合"尽量不用代码生成"原则）。
package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"fusion/dialect"
)

// Catalog 是一次内省的结果集：表名 → Table。
// 进程内缓存，支持手动 Refresh 重建。
type Catalog struct {
	mu      sync.RWMutex
	Dialect dialect.Dialect
	tables  map[string]*Table
}

// Table 描述一张数据库表的结构（来自内省）。
type Table struct {
	Name        string
	Columns     []Column
	PrimaryKey  []string    // []string 支持复合主键
	ForeignKeys []ForeignKey
	Indexes     []Index
}

// Column 描述一列的数据库侧信息。
type Column struct {
	Name     string  // 列名
	SQLType  string  // 数据库原生类型（如 INTEGER / varchar(255) / jsonb）
	Nullable bool    // 是否允许 NULL
	Default  *string // 列默认值表达式（原样字符串，nil=无默认值或 NULL）
}

// ForeignKey 描述一个外键约束。
// 单列外键：Column 和 RefColumns 各一个元素；复合外键：多个（复合外键当前仅记录，
// AutoRegisterRelations 会跳过，留待后续支持）。
type ForeignKey struct {
	Name       string   // 约束名
	Column     string   // 本表外键列
	RefTable   string   // 引用表名
	RefColumns []string // 引用表列名
}

// Index 描述一个索引。
type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// Queryer 是内省所需的最小查询接口（*sql.DB / *sql.Tx 满足）。
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Introspector 由各方言实现，负责从数据库读取单表/表列表的结构。
type Introspector interface {
	// DescribeTable 内省单张表，返回其列/主键/外键/索引。
	DescribeTable(ctx context.Context, q Queryer, table string) (*Table, error)
	// ListTables 列出当前 schema 下所有用户表（排除系统表/视图）。
	ListTables(ctx context.Context, q Queryer) ([]string, error)
}

// IntrospectorFor 按方言返回对应的 Introspector。
// 未知方言返回错误。
func IntrospectorFor(d dialect.Dialect) (Introspector, error) {
	switch d.Name() {
	case "sqlite":
		return sqliteIntrospector{}, nil
	case "postgres":
		return postgresIntrospector{}, nil
	case "mysql":
		return mysqlIntrospector{}, nil
	}
	return nil, fmt.Errorf("fusion: schema: no introspector for dialect %q", d.Name())
}

// Load 内省指定表集合，构建 Catalog 缓存。
// tables 为空则内省当前 schema 下所有用户表（调 ListTables）。
func Load(ctx context.Context, q Queryer, d dialect.Dialect, tables ...string) (*Catalog, error) {
	isp, err := IntrospectorFor(d)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		tables, err = isp.ListTables(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("fusion: schema: list tables: %w", err)
		}
	}
	cat := &Catalog{Dialect: d, tables: make(map[string]*Table, len(tables))}
	for _, t := range tables {
		tab, err := isp.DescribeTable(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("fusion: schema: describe %q: %w", t, err)
		}
		cat.tables[t] = tab
	}
	return cat, nil
}

// Table 按表名查询内省结果，不存在返回 nil。
func (c *Catalog) Table(name string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tables[name]
}

// Tables 返回所有内省表名（顺序不保证）。
func (c *Catalog) Tables() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.tables))
	for name := range c.tables {
		out = append(out, name)
	}
	return out
}

// AllTables 返回所有内省 Table（顺序不保证）。
func (c *Catalog) AllTables() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Table, 0, len(c.tables))
	for _, t := range c.tables {
		out = append(out, t)
	}
	return out
}

// PKColumns 返回表的主键列名集合；表不存在或无主键返回 nil。
func (t *Table) PKColumns() []string {
	if t == nil {
		return nil
	}
	return t.PrimaryKey
}

// Column 按列名查找，不存在返回 nil。
func (t *Table) Column(name string) *Column {
	if t == nil {
		return nil
	}
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}
