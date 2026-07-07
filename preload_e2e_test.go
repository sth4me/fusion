package fusion_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/rel"
)

// Preload 测试模型
type PUser struct {
	ID       col.Col[int64]
	Name     col.Col[string]
	DeptID   col.Col[int64]
	Dept     rel.Rel[PDept]
	Profile  rel.Rel[PProfile]
	Posts    rel.RelMany[PPost]
}

type PDept struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

type PProfile struct {
	ID  col.Col[int64]
	UID col.Col[int64] // has_one 外键
	Bio col.Col[string]
}

type PPost struct {
	ID    col.Col[int64]
	UID   col.Col[int64] // has_many 外键
	Title col.Col[string]
}

func setupPreloadDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db := openSQLite(t)
	// 建表
	mustExecP(db, `CREATE TABLE pusers (id INTEGER PRIMARY KEY, name TEXT, dept_id INTEGER)`)
	mustExecP(db, `CREATE TABLE pdepts (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExecP(db, `CREATE TABLE pprofiles (id INTEGER PRIMARY KEY, uid INTEGER, bio TEXT)`)
	mustExecP(db, `CREATE TABLE pposts (id INTEGER PRIMARY KEY, uid INTEGER, title TEXT)`)
	// 数据
	mustExecP(db, `INSERT INTO pdepts VALUES (1,'工程部'),(2,'市场部')`)
	mustExecP(db, `INSERT INTO pusers VALUES (1,'alice',1),(2,'bob',1),(3,'carol',2)`)
	mustExecP(db, `INSERT INTO pprofiles VALUES (10,1,'alice bio')`) // 只有 alice 有 profile
	mustExecP(db, `INSERT INTO pposts VALUES (100,1,'alice post1'),(101,1,'alice post2'),(102,2,'bob post')`)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	// 注册模型（先子后父）
	Depts := fusion.Register[PDept]("pdepts")
	Profiles := fusion.Register[PProfile]("pprofiles")
	Posts := fusion.Register[PPost]("pposts")
	Users := fusion.Register[PUser]("pusers")

	// 注册关联
	fusion.HasMany(
		func(u *PUser) any { return &u.Posts },
		func(p *PPost) any { return &p.UID },
		func(u *PUser) any { return &u.ID },
	)
	fusion.BelongsTo(
		func(u *PUser) any { return &u.Dept },
		func(u *PUser) any { return &u.DeptID },
		func(d *PDept) any { return &d.ID },
	)
	fusion.HasOne(
		func(u *PUser) any { return &u.Profile },
		func(p *PProfile) any { return &p.UID },
		func(u *PUser) any { return &u.ID },
	)
	_ = Depts
	_ = Profiles
	_ = Posts
	_ = Users
	return fusion.WrapDB(db), db
}

func mustExecP(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		panic(err)
	}
}

// TestPreload_HasMany 验证 has_many 预加载 + 分组回填
func TestPreload_HasMany(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, err := fusion.From(Users, wrapped).
		Preload("Posts").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("got %d users, want 3", len(users))
	}
	// alice(1) 有 2 posts，bob(2) 有 1，carol(3) 有 0
	alice := users[0]
	if !alice.Posts.Loaded() {
		t.Fatal("alice.Posts should be loaded")
	}
	posts, _ := alice.Posts.All()
	if len(posts) != 2 {
		t.Errorf("alice posts got %d, want 2", len(posts))
	}
	bob := users[1]
	bobPosts, _ := bob.Posts.All()
	if len(bobPosts) != 1 {
		t.Errorf("bob posts got %d, want 1", len(bobPosts))
	}
	carol := users[2]
	carolPosts, _ := carol.Posts.All()
	if len(carolPosts) != 0 {
		t.Errorf("carol posts got %d, want 0", len(carolPosts))
	}
	// carol 的 Posts 应是"加载了但空"
	if !carol.Posts.Loaded() {
		t.Error("carol.Posts should be Loaded (even if empty)")
	}
}

// TestPreload_BelongsTo 验证 belongs_to 预加载
func TestPreload_BelongsTo(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, _ := fusion.From(Users, wrapped).
		Preload("Dept").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())

	for _, u := range users {
		if !u.Dept.Loaded() {
			t.Fatal("Dept should be loaded")
		}
	}
	// alice 和 bob 在工程部(1)，carol 在市场部(2)
	if users[0].Dept.IsNil() || users[0].Dept.MustGet().Name.Get() != "工程部" {
		t.Errorf("alice dept got %+v", users[0].Dept.MustGet())
	}
	if users[2].Dept.IsNil() || users[2].Dept.MustGet().Name.Get() != "市场部" {
		t.Errorf("carol dept got %+v", users[2].Dept.MustGet())
	}
}

// TestPreload_HasOne 验证 has_one 预加载（部分无关联）
func TestPreload_HasOne(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, _ := fusion.From(Users, wrapped).
		Preload("Profile").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())

	// alice(1) 有 profile，bob(2)/carol(3) 无
	if !users[0].Profile.Loaded() {
		t.Fatal("alice.Profile should be loaded")
	}
	if users[0].Profile.IsNil() {
		t.Error("alice should have profile")
	} else if users[0].Profile.MustGet().Bio.Get() != "alice bio" {
		t.Errorf("alice bio got %q", users[0].Profile.MustGet().Bio.Get())
	}
	// bob/carol：加载了但无
	for _, u := range users[1:] {
		if !u.Profile.Loaded() {
			t.Error("Profile should be loaded for all")
		}
		if !u.Profile.IsNil() {
			t.Error("bob/carol should have nil profile")
		}
	}
}

// TestPreload_Multiple 同时预加载多个关联
func TestPreload_Multiple(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, _ := fusion.From(Users, wrapped).
		Preload("Posts", "Dept", "Profile").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())

	alice := users[0]
	if !alice.Posts.Loaded() || !alice.Dept.Loaded() || !alice.Profile.Loaded() {
		t.Error("all relations should be loaded")
	}
	posts, _ := alice.Posts.All()
	if len(posts) != 2 {
		t.Errorf("posts got %d, want 2", len(posts))
	}
}

// TestPreload_NotPreloadedNotLoaded 验证不 Preload 时关联未加载
func TestPreload_NotPreloadedNotLoaded(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, _ := fusion.From(Users, wrapped).All(context.Background())
	alice := users[0]
	if alice.Posts.Loaded() {
		t.Error("Posts should NOT be loaded without Preload")
	}
	_, err := alice.Posts.All()
	if !errors.Is(err, rel.ErrNotLoaded) {
		t.Errorf("All on unloaded should return ErrNotLoaded, got %v", err)
	}
}

// TestPreload_One 单实体 Preload
func TestPreload_One(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	alice, err := fusion.From(Users, wrapped).
		Preload("Posts").
		Where(Users.Proto.ID.Eq(1)).
		One(context.Background())
	if err != nil {
		t.Fatalf("one: %v", err)
	}
	if !alice.Posts.Loaded() {
		t.Fatal("alice.Posts should be loaded via One+Preload")
	}
	posts, _ := alice.Posts.All()
	if len(posts) != 2 {
		t.Errorf("got %d posts, want 2", len(posts))
	}
}

// TestPreload_EmptyResult 空结果集 Preload 不报错
func TestPreload_EmptyResult(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	users, err := fusion.From(Users, wrapped).
		Preload("Posts").
		Where(Users.Proto.ID.Eq(999)). // 不存在
		All(context.Background())
	if err != nil {
		t.Fatalf("empty result with preload: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("got %d, want 0", len(users))
	}
}

// TestPreload_Nested 嵌套预加载（Post 本身有关联）—— MVP 验证单层，嵌套留待后续
// 此处验证单层 Preload 在事务内工作
func TestPreload_InTransaction(t *testing.T) {
	wrapped, raw := setupPreloadDB(t)
	defer raw.Close()
	Users := fusion.Register[PUser]("pusers")

	err := fusion.Tx(context.Background(), raw, func(ctx context.Context) error {
		users, err := fusion.From(Users, wrapped).
			Preload("Posts").
			OrderBy(Users.Proto.ID.Asc()).
			All(ctx)
		if err != nil {
			return err
		}
		alice := users[0]
		posts, _ := alice.Posts.All()
		if len(posts) != 2 {
			return errors.New("expected 2 posts in tx")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("preload in tx: %v", err)
	}
}
