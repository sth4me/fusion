// Package main 演示 fusion 的反向迁移：从数据库 schema 推导运行时元数据，
// 自动校验模型漂移 + 从外键自动注册关联（零手写 HasMany/BelongsTo）。
//
// 运行：cd examples/reverse && go run .
// 需要内存 SQLite（已在 go.mod 引入 modernc.org/sqlite）。
//
// 本例对应 docs 中"反向迁移（DB → 元数据）"路线：
// 不生成 .go 源码，而是在运行时构建 schema.Catalog 缓存，
// 与 meta.ModelMeta（反射侧）互补。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/rel"

	_ "modernc.org/sqlite"
)

// 模型定义。注意：无需手写任何 fusion.HasMany/BelongsTo。
// ADept 上的 APosts 字段（APost 复数化）和 APost 上的 Dept 字段（dept_id 去后缀）
// 都会由 AutoRegisterRelations 根据数据库外键自动注册。
type ADept struct {
	ID     col.Col[int64]
	Name   col.Col[string]
	APosts rel.RelMany[APost] // 约定字段名：子模型类型名复数化
}

type APost struct {
	ID     col.Col[int64]
	DeptID col.Col[int64] // FK 列 dept_id
	Title  col.Col[string]
	Dept   rel.Rel[ADept] // 约定字段名：dept_id 去后缀 → Dept
}

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	// 建带外键的表（DDL 是真理，模型从中反向推导）
	mustExec(db, `CREATE TABLE adepts (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	mustExec(db, `CREATE TABLE aposts (
		id INTEGER PRIMARY KEY,
		dept_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		CONSTRAINT fk_dept FOREIGN KEY (dept_id) REFERENCES adepts(id))`)
	mustExec(db, `INSERT INTO adepts VALUES (1,'工程'),(2,'市场')`)
	mustExec(db, `INSERT INTO aposts VALUES (10,1,'p1'),(11,1,'p2'),(12,2,'p3')`)

	ctx := context.Background()
	wrapped := fusion.WrapDB(db)

	// 1) 注册模型（仅声明类型，不写任何关联注册）
	Depts := fusion.Register[ADept]("adepts")
	Posts := fusion.Register[APost]("aposts")

	// 2) 内省数据库 schema → Catalog
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "adepts", "aposts")
	if err != nil {
		log.Fatalf("LoadSchema: %v", err)
	}
	fmt.Println("== schema 内省结果 ==")
	for _, name := range cat.Tables() {
		t := cat.Table(name)
		names := make([]string, 0, len(t.Columns))
		for _, c := range t.Columns {
			names = append(names, c.Name)
		}
		fmt.Printf("  %s: 列 %v, 主键 %v, 外键数 %d\n", name, names, t.PrimaryKey, len(t.ForeignKeys))
		for _, fk := range t.ForeignKeys {
			fmt.Printf("    FK %s.%s → %s.%v\n", name, fk.Column, fk.RefTable, fk.RefColumns)
		}
	}

	// 3) 校验模型 vs schema 是否漂移（一致时 MustBind 静默；漂移则 panic）
	fusion.MustBind(cat, Depts)
	fusion.MustBind(cat, Posts)
	fmt.Println("\n== Bind 校验通过（模型与 schema 一致）==")

	// 4) 从外键自动注册关联（手动优先：这里没手写，全靠外键）
	fusion.AutoRegisterRelations(cat)
	fmt.Println("== AutoRegisterRelations 已从外键注册关联（零手写）==")

	// 5) 直接 Preload，证明自动注册的关联等价于手写 HasMany/BelongsTo
	fmt.Println("\n== Preload 演示（has_many + belongs_to 都可用）==")
	posts, err := fusion.From(Posts, wrapped).
		Preload("Dept"). // belongs_to：post → dept
		OrderBy(Posts.Proto.ID.Asc()).
		All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range posts {
		d := p.Dept.MustGet()
		fmt.Printf("  post %d (%s) 属于 dept %d (%s)\n", p.ID.Get(), p.Title.Get(), d.ID.Get(), d.Name.Get())
	}

	depts, err := fusion.From(Depts, wrapped).
		Preload("APosts"). // has_many：dept → posts
		OrderBy(Depts.Proto.ID.Asc()).
		All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range depts {
		ps, _ := d.APosts.All()
		fmt.Printf("  dept %d (%s) 有 %d 篇 post\n", d.ID.Get(), d.Name.Get(), len(ps))
	}

	fmt.Println("\n提示：若数据库不设外键，AutoRegisterRelations no-op，完全靠手写 fusion.HasMany/BelongsTo；")
	fmt.Println("     若用户手写了关联，AutoRegisterRelations 不覆盖（手动优先）。")
}

func mustExec(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		log.Fatalf("exec %q: %v", q, err)
	}
}
