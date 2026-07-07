package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/rel"
	"github.com/sth4me/fusion/relation"

	_ "modernc.org/sqlite"
)

// 自动关联测试模型。命名遵循约定：
//   - APost.DeptID (col, FK 列 dept_id) → ADept (belongs_to 字段名 "Dept")
//   - ADept 上 has_many 字段名 "APosts"（表名 aposts 复数化 PascalCase）
type ADept struct {
	ID     col.Col[int64]
	Name   col.Col[string]
	APosts rel.RelMany[APost] // 约定字段：aposts 复数化
}

type APost struct {
	ID     col.Col[int64]
	DeptID col.Col[int64] // FK 列 dept_id
	Title  col.Col[string]
	Dept   rel.Rel[ADept] // 约定字段：dept_id 去后缀 → Dept
}

func openAutoRelDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// SQLite 外键需显式启用才参与 introspection（PRAGMA foreign_key_list 仍可读，
	// 无需 PRAGMA foreign_keys=ON；FK 定义在 DDL 里即可被 foreign_key_list 读出）
	if _, err := db.Exec(`CREATE TABLE adepts (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create adepts: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE aposts (
		id INTEGER PRIMARY KEY,
		dept_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		CONSTRAINT fk_dept FOREIGN KEY (dept_id) REFERENCES adepts(id))`); err != nil {
		t.Fatalf("create aposts: %v", err)
	}
	// 数据：dept 1 有 2 posts，dept 2 有 1 post
	for _, q := range []string{
		`INSERT INTO adepts VALUES (1,'工程'),(2,'市场')`,
		`INSERT INTO aposts VALUES (10,1,'p1'),(11,1,'p2'),(12,2,'p3')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return db
}

// TestE2E_AutoRegisterRelations_BelongsTo 自动注册的 belongs_to 能 Preload。
func TestE2E_AutoRegisterRelations_BelongsTo(t *testing.T) {
	db := openAutoRelDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Depts := fusion.Register[ADept]("adepts")
	Posts := fusion.Register[APost]("aposts")

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "adepts", "aposts")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	// 自动注册（无任何手写 HasMany/BelongsTo）
	fusion.AutoRegisterRelations(cat)

	// 验证 belongs_to 已注册：relation.Lookup 应非 nil
	if relation.Lookup(Posts.Meta.Type, "Dept") == nil {
		t.Fatal("auto belongs_to 'Dept' not registered on APost")
	}

	// Preload("Dept") 应正确加载
	posts, err := fusion.From(Posts, fusion.WrapDB(db)).
		Preload("Dept").
		OrderBy(Posts.Proto.ID.Asc()).
		All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(posts) != 3 {
		t.Fatalf("got %d posts, want 3", len(posts))
	}
	// post 10/11 → 工程部, post 12 → 市场
	if posts[0].Dept.IsNil() || posts[0].Dept.MustGet().Name.Get() != "工程" {
		t.Errorf("post 10 dept got %+v", posts[0].Dept)
	}
	if posts[2].Dept.IsNil() || posts[2].Dept.MustGet().Name.Get() != "市场" {
		t.Errorf("post 12 dept got %+v", posts[2].Dept)
	}
	_ = Depts
}

// TestE2E_AutoRegisterRelations_HasMany 自动注册的 has_many 能 Preload。
func TestE2E_AutoRegisterRelations_HasMany(t *testing.T) {
	db := openAutoRelDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Depts := fusion.Register[ADept]("adepts")
	_ = fusion.Register[APost]("aposts") // 子类型必须注册，AutoRegisterRelations 才能解析

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "adepts", "aposts")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	fusion.AutoRegisterRelations(cat)

	// has_many 字段名 "APosts"（aposts → APosts）
	if relation.Lookup(Depts.Meta.Type, "APosts") == nil {
		t.Fatal("auto has_many 'APosts' not registered on ADept")
	}

	depts, err := fusion.From(Depts, fusion.WrapDB(db)).
		Preload("APosts").
		OrderBy(Depts.Proto.ID.Asc()).
		All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	dept1Posts, _ := depts[0].APosts.All()
	if len(dept1Posts) != 2 {
		t.Errorf("dept 1 (工程) got %d posts, want 2", len(dept1Posts))
	}
	dept2Posts, _ := depts[1].APosts.All()
	if len(dept2Posts) != 1 {
		t.Errorf("dept 2 (市场) got %d posts, want 1", len(dept2Posts))
	}
}

// TestE2E_AutoRegisterRelations_ManualPriority 手写 HasMany 后自动注册不覆盖。
// 这验证"手动优先"铁律：用户显式声明的关联优先。
func TestE2E_AutoRegisterRelations_ManualPriority(t *testing.T) {
	db := openAutoRelDB(t)
	defer db.Close()
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Depts := fusion.Register[ADept]("adepts")
	_ = fusion.Register[APost]("aposts")

	// 手动注册一个 has_many 到字段 "APosts"（与自动约定同名）
	manual := fusion.HasMany(
		func(d *ADept) any { return &d.APosts },
		func(p *APost) any { return &p.DeptID },
		func(d *ADept) any { return &d.ID },
	)

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "adepts", "aposts")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	fusion.AutoRegisterRelations(cat)

	// 自动注册后，(ADept, "APosts") 应仍是手动那个实例（指针相等）
	got := relation.Lookup(Depts.Meta.Type, "APosts")
	if got != manual {
		t.Error("manual HasMany should NOT be overwritten by auto registration")
	}
	// 仍能正常 Preload
	depts, err := fusion.From(Depts, fusion.WrapDB(db)).
		Preload("APosts").
		OrderBy(Depts.Proto.ID.Asc()).
		All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	dept1Posts, _ := depts[0].APosts.All()
	if len(dept1Posts) != 2 {
		t.Errorf("manual has_many should still preload, got %d posts", len(dept1Posts))
	}
}

// TestE2E_AutoRegisterRelations_NoFK 无外键的表 → no-op，不报错。
func TestE2E_AutoRegisterRelations_NoFK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE standalone (id INTEGER PRIMARY KEY, val TEXT)`); err != nil {
		t.Fatal(err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	_ = fusion.Register[struct {
		ID  col.Col[int64]
		Val col.Col[string]
	}]("standalone")

	ctx := context.Background()
	cat, err := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "standalone")
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	// 无外键：no-op，不应 panic
	fusion.AutoRegisterRelations(cat)
}
