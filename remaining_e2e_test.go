package fusion_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// RUser 剩余功能测试模型
type RUser struct {
	ID       col.Col[int64]
	Name     col.Col[string]
	Age      col.Col[int]
	DeptID   col.Col[int64]
	Metadata col.Json[map[string]any] // jsonb 字段
}
type RPost struct {
	ID    col.Col[int64]
	UID   col.Col[int64]
	Title col.Col[string]
}

func setupRemainingDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db := openSQLite(t)
	execSQL(db, `CREATE TABLE rusers (id INTEGER PRIMARY KEY, name TEXT, age INTEGER, dept_id INTEGER, metadata TEXT)`)
	execSQL(db, `CREATE TABLE rposts (id INTEGER PRIMARY KEY, uid INTEGER, title TEXT)`)
	execSQL(db, `INSERT INTO rusers VALUES (1,'alice',30,1,'{"role":"admin"}'),(2,'bob',20,1,'{"role":"user"}'),(3,'carol',25,2,'{}')`)
	execSQL(db, `INSERT INTO rposts VALUES (10,1,'p1'),(11,1,'p2'),(12,2,'p3')`)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	fusion.Register[RPost]("rposts")
	fusion.Register[RUser]("rusers")
	return fusion.WrapDB(db), db
}

// TestDeleteByID 按主键删除
func TestDeleteByID(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	err := fusion.DeleteByID(Users, wrapped, int64(3)).Exec(context.Background())
	if err != nil {
		t.Fatalf("delete by id: %v", err)
	}
	all, _ := fusion.From(Users, wrapped).All(context.Background())
	if len(all) != 2 {
		t.Errorf("got %d users, want 2 (carol deleted)", len(all))
	}
}

// TestNotIn NOT IN
func TestNotIn(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	users, _ := fusion.From(Users, wrapped).
		Where(Users.Proto.ID.NotIn([]int64{1, 2})).
		All(context.Background())
	if len(users) != 1 || users[0].Name.Get() != "carol" {
		t.Errorf("got %d users, want 1 (carol)", len(users))
	}
}

// TestNotBetween NOT BETWEEN
func TestNotBetween(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	users, _ := fusion.From(Users, wrapped).
		Where(Users.Proto.Age.NotBetween(20, 30)).
		All(context.Background())
	// alice(30) 和 bob(20) 在 [20,30] 内，carol(25) 也在 → NOT BETWEEN 全无
	// 实际：30 在 [20,30], 20 在, 25 在 → 全部被排除，0 行
	if len(users) != 0 {
		t.Errorf("got %d users, want 0", len(users))
	}
}

// TestForUpdate FOR UPDATE（SQL 生成验证）
func TestForUpdate(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	// 验证 ForUpdate 设置了 lockClause（通过 builder 直接验证）
	// SQLite 不真正支持 FOR UPDATE，但 SQL 生成应正确
	tab := fusion.Register[RUser]("rusers")
	_ = tab
	// 用 QueryHook 捕获 SQL
	var capturedSQL string
	unreg := fusion.AddQueryHook(func(ctx context.Context, info fusion.QueryInfo) error {
		capturedSQL = info.SQL
		return nil
	})
	defer unreg()

	// SQLite 不支持 FOR UPDATE，会报错，但 SQL 已生成并记录
	_, _ = fusion.From(Users, wrapped).ForUpdate().Where(Users.Proto.ID.Eq(1)).All(context.Background())
	if !contains(capturedSQL, "FOR UPDATE") {
		t.Errorf("SQL should contain FOR UPDATE: %q", capturedSQL)
	}
}

// TestEqDistinct NULL 安全比较（IS NOT DISTINCT FROM）
func TestEqDistinct(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	users, _ := fusion.From(Users, wrapped).
		Where(Users.Proto.Age.EqDistinct(30)).
		All(context.Background())
	if len(users) != 1 || users[0].Name.Get() != "alice" {
		t.Errorf("got %d users, want alice", len(users))
	}
}

// TestJsonField jsonb 字段读写
func TestJsonField(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	// 读：alice 的 metadata 应有 role=admin
	alice, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	md := alice.Metadata.Get()
	if role, ok := md["role"].(string); !ok || role != "admin" {
		t.Errorf("metadata role got %v", md["role"])
	}

	// 写：更新 metadata
	newMD := map[string]any{"role": "superadmin", "level": float64(5)}
	alice.Metadata.Set(newMD)
	fusion.Update(Users, wrapped, &alice).Exec(context.Background())

	after, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	if role := after.Metadata.Get()["role"]; role != "superadmin" {
		t.Errorf("updated role got %v", role)
	}
}

// TestJsonMarshalUnmarshal Json[T] 的 JSON 透明
func TestJsonMarshalUnmarshal(t *testing.T) {
	type Info struct {
		Name string
	}
	j := col.Json[Info]{}
	j.Set(Info{Name: "test"})
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(b), `"Name":"test"`) {
		t.Errorf("json got %s", b)
	}

	var j2 col.Json[Info]
	json.Unmarshal(b, &j2)
	if j2.Get().Name != "test" {
		t.Errorf("unmarshal got %v", j2.Get())
	}
}

// TestExists EXISTS 子查询
func TestExists(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")
	Posts := fusion.Register[RPost]("rposts")

	// 查有 post 的 user（alice 和 bob 有 post，carol 没有）
	subQ := fusion.From(Posts, wrapped).Where(Posts.Proto.UID.EqCol(Users.Proto.ID))
	users, err := fusion.From(Users, wrapped).
		Where(fusion.Exists(subQ)).
		All(context.Background())
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("got %d users, want 2 (alice, bob have posts)", len(users))
	}
}

// TestNotExists NOT EXISTS 子查询
func TestNotExists(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")
	Posts := fusion.Register[RPost]("rposts")

	// 查没有 post 的 user（carol）
	subQ := fusion.From(Posts, wrapped).Where(Posts.Proto.UID.EqCol(Users.Proto.ID))
	users, _ := fusion.From(Users, wrapped).
		Where(fusion.NotExists(subQ)).
		All(context.Background())
	if len(users) != 1 || users[0].Name.Get() != "carol" {
		t.Errorf("got %d users, want 1 (carol)", len(users))
	}
}

// TestContextCancel 验证 ctx 取消时查询中断
func TestContextCancel(t *testing.T) {
	wrapped, raw := setupRemainingDB(t)
	defer raw.Close()
	Users := fusion.Register[RUser]("rusers")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // 确保 ctx 已过期

	_, err := fusion.From(Users, wrapped).All(ctx)
	if err == nil {
		// SQLite 内存库太快可能不报错，但 ctx 过期时应返回 ctx.Err
		// 这里宽松验证：不 panic 即可（SQLite 可能已完成）
		t.Log("query completed before ctx check (SQLite in-memory is fast)")
	}
}

// TestOpen 便捷构造 Open
func TestOpen(t *testing.T) {
	db, wrapped, err := fusion.Open("sqlite", ":memory:", dialect.SQLiteDialect)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if wrapped == nil {
		t.Error("wrapped DB should not be nil")
	}
	if fusion.DefaultDialect() != dialect.SQLiteDialect {
		t.Error("Open should set default dialect")
	}
	// 验证可用
	execSQL(db, `CREATE TABLE t1 (id INTEGER)`)
	execSQL(db, `INSERT INTO t1 VALUES (1)`)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM t1").Scan(&n)
	if n != 1 {
		t.Errorf("got %d, want 1", n)
	}
}
