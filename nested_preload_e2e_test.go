package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
	"fusion/rel"
)

// 嵌套预加载测试模型（独立模型，避免与 preload_e2e_test 的 PPost 缓存冲突）。
type NUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Posts rel.RelMany[NPost]
}

type NPost struct {
	ID       col.Col[int64]
	UID      col.Col[int64] // has_many 外键 → NUser
	Title    col.Col[string]
	Comments rel.RelMany[NComment]
}

type NComment struct {
	ID     col.Col[int64]
	PostID col.Col[int64] // has_many 外键 → NPost
	Body   col.Col[string]
}

func setupNestedPreloadDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, ddl := range []string{
		`CREATE TABLE nusers (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE nposts (id INTEGER PRIMARY KEY, uid INTEGER, title TEXT)`,
		`CREATE TABLE ncomments (id INTEGER PRIMARY KEY, post_id INTEGER, body TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	// alice(1) 有 p1(10)、p2(11)；bob(2) 有 p3(12)。
	// p1 有两条评论 c1(20)、c2(21)；p2 无评论；p3 有一条 c3(22)。
	for _, q := range []string{
		`INSERT INTO nusers VALUES (1,'alice'),(2,'bob')`,
		`INSERT INTO nposts VALUES (10,1,'p1'),(11,1,'p2'),(12,2,'p3')`,
		`INSERT INTO ncomments VALUES (20,10,'c1'),(21,10,'c2'),(22,12,'c3')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Comments := fusion.Register[NComment]("ncomments")
	Posts := fusion.Register[NPost]("nposts")
	Users := fusion.Register[NUser]("nusers")

	// User → Posts (has_many)
	fusion.HasMany(
		func(u *NUser) any { return &u.Posts },
		func(p *NPost) any { return &p.UID },
		func(u *NUser) any { return &u.ID },
	)
	// Post → Comments (has_many)
	fusion.HasMany(
		func(p *NPost) any { return &p.Comments },
		func(c *NComment) any { return &c.PostID },
		func(p *NPost) any { return &p.ID },
	)
	_ = Comments
	_ = Posts
	_ = Users
	return fusion.WrapDB(db), db
}

// TestNestedPreload 验证点号路径嵌套预加载（User→Posts→Comments）。
func TestNestedPreload(t *testing.T) {
	wrapped, raw := setupNestedPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[NUser]("nusers")

	users, err := fusion.From(Users, wrapped).
		Preload("Posts.Comments").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}

	alice := users[0]
	posts, err := alice.Posts.All()
	if err != nil {
		t.Fatalf("alice.Posts.All: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("alice got %d posts, want 2", len(posts))
	}
	// 验证每个 post 的 Comments 都被加载了
	for _, p := range posts {
		if !p.Comments.Loaded() {
			t.Errorf("post %d: Comments should be loaded via nested Preload", p.ID.Get())
		}
	}
	// p1(10) 应有 2 条评论，p2(11) 应有 0 条
	var p1, p2 *NPost
	for i := range posts {
		switch posts[i].ID.Get() {
		case 10:
			p1 = &posts[i]
		case 11:
			p2 = &posts[i]
		}
	}
	if p1 == nil || p2 == nil {
		t.Fatalf("missing p1/p2")
	}
	c1, _ := p1.Comments.All()
	if len(c1) != 2 {
		t.Errorf("p1 comments got %d, want 2", len(c1))
	}
	c2, _ := p2.Comments.All()
	if len(c2) != 0 {
		t.Errorf("p2 comments got %d, want 0", len(c2))
	}
	if !p2.Comments.Loaded() {
		t.Error("p2.Comments should be Loaded (even if empty)")
	}
}

// TestNestedPreloadSingleLevel 验证不带点号的路径退化为单层（不破坏现有语义）。
func TestNestedPreloadSingleLevel(t *testing.T) {
	wrapped, raw := setupNestedPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[NUser]("nusers")

	users, err := fusion.From(Users, wrapped).
		Preload("Posts").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// 单层：Posts 已加载，但 Posts 的 Comments 未加载
	posts, _ := users[0].Posts.All()
	if len(posts) == 0 {
		t.Fatal("alice should have posts")
	}
	if posts[0].Comments.Loaded() {
		t.Error("Comments should NOT be loaded without nested path")
	}
}

// TestNestedPreloadOne 验证 One()+嵌套 Preload。
func TestNestedPreloadOne(t *testing.T) {
	wrapped, raw := setupNestedPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[NUser]("nusers")

	alice, err := fusion.From(Users, wrapped).
		Preload("Posts.Comments").
		Where(Users.Proto.ID.Eq(1)).
		One(context.Background())
	if err != nil {
		t.Fatalf("one: %v", err)
	}
	posts, _ := alice.Posts.All()
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}
	for _, p := range posts {
		if !p.Comments.Loaded() {
			t.Errorf("post %d Comments not loaded", p.ID.Get())
		}
	}
}
