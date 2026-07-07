package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/rel"
)

// m2m 测试模型：User ↔ Tag 经 user_tags 连接表。
type MUser struct {
	ID   col.Col[int64]
	Name col.Col[string]
	Tags rel.RelMany[MTag]
}

type MTag struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

// MUserTag 连接表（左 FK→user，右 FK→tag）。
type MUserTag struct {
	UserID col.Col[int64]
	TagID  col.Col[int64]
}

func setupM2MDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE musers (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE mtags (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE muser_tags (user_id INTEGER, tag_id INTEGER)`,
		`INSERT INTO musers VALUES (1,'alice'),(2,'bob')`,
		`INSERT INTO mtags VALUES (10,'go'),(11,'orm'),(12,'pg')`,
		// alice(1) 有 go(10)、orm(11)；bob(2) 有 go(10)、pg(12)
		`INSERT INTO muser_tags VALUES (1,10),(1,11),(2,10),(2,12)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return fusion.WrapDB(db), db
}

// TestE2M_M2M_Preload 多对多 Preload 完整功能验证。
func TestE2M_M2M_Preload(t *testing.T) {
	wrapped, raw := setupM2MDB(t)
	defer raw.Close()
	_ = fusion.Register[MTag]("mtags")         // 子类型必须注册（m2m 解析子类型用）
	_ = fusion.Register[MUserTag]("muser_tags") // 连接表必须注册
	Users := fusion.Register[MUser]("musers")

	// 注册 m2m：User.Tags，经 MUserTag 连接，左 FK=user_id（→user），右 FK=tag_id（→tag）
	fusion.ManyToMany(
		func(u *MUser) any { return &u.Tags },
		func(j *MUserTag) any { return &j.UserID }, // 连接表指向父(user)的外键
		func(j *MUserTag) any { return &j.TagID },  // 连接表指向子(tag)的外键
		func(u *MUser) any { return &u.ID },         // 父主键
		func(tg *MTag) any { return &tg.ID },        // 子主键
	)

	users, err := fusion.From(Users, wrapped).
		Preload("Tags").
		OrderBy(Users.Proto.ID.Asc()).
		All(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}

	// alice 应有 2 tags（go, orm）
	if !users[0].Tags.Loaded() {
		t.Fatal("alice.Tags should be loaded")
	}
	aliceTags, _ := users[0].Tags.All()
	if len(aliceTags) != 2 {
		t.Fatalf("alice got %d tags, want 2", len(aliceTags))
	}
	tagIDs := map[int64]bool{}
	for _, tg := range aliceTags {
		tagIDs[tg.ID.Get()] = true
	}
	if !tagIDs[10] || !tagIDs[11] {
		t.Errorf("alice tags should be go(10)+orm(11), got IDs %v", tagIDs)
	}

	// bob 应有 2 tags（go, pg）
	bobTags, _ := users[1].Tags.All()
	if len(bobTags) != 2 {
		t.Fatalf("bob got %d tags, want 2", len(bobTags))
	}
	// 验证连接表多对多正确性：go(10) 同时属于 alice 和 bob（共享 tag）
	bobTagIDs := map[int64]bool{}
	for _, tg := range bobTags {
		bobTagIDs[tg.ID.Get()] = true
	}
	if !bobTagIDs[10] || !bobTagIDs[12] {
		t.Errorf("bob tags should be go(10)+pg(12), got IDs %v", bobTagIDs)
	}
}
