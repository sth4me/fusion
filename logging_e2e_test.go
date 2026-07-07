package fusion_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// LogUser 用于日志测试的模型
type LogUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func setupLogDB(t *testing.T) (fusion.DB, *sql.DB) {
	db := openSQLite(t)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	fusion.Register[LogUser]("users")
	return fusion.WrapDB(db), db
}

// captureLogger 替换全局 logger 为写 buffer 的，返回 buffer 与恢复函数。
func captureFusionLogger(level slog.Level) (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	orig := fusion.Logger()
	fusion.SetLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	return buf, func() { fusion.SetLogger(orig) }
}

// TestE2E_LogSQL 验证 Debug 级 logger 记录完整 SQL
func TestE2E_LogSQL(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	Users := fusion.Register[LogUser]("users")

	buf, restore := captureFusionLogger(slog.LevelDebug)
	defer restore()

	_, _ = fusion.From(Users, wrapped).Where(Users.Proto.Name.Eq("alice")).All(context.Background())

	out := buf.String()
	if !strings.Contains(out, "level=DEBUG") {
		t.Errorf("should log DEBUG, got %s", out)
	}
	if !strings.Contains(out, `op=SELECT`) {
		t.Errorf("should contain op=SELECT, got %s", out)
	}
	if !strings.Contains(out, "FROM") {
		t.Errorf("should contain SQL, got %s", out)
	}
}

// TestE2E_LogInsertRowsAffected 验证 INSERT 日志含 RowsAffected=1
func TestE2E_LogInsertRowsAffected(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	Users := fusion.Register[LogUser]("users")

	buf, restore := captureFusionLogger(slog.LevelDebug)
	defer restore()

	u := &LogUser{}
	u.Name.Set("alice")
	u.Age.Set(30)
	_ = fusion.Insert(Users, wrapped, u).Exec(context.Background())

	out := buf.String()
	if !strings.Contains(out, "op=INSERT") {
		t.Errorf("should log INSERT, got %s", out)
	}
	if !strings.Contains(out, "rows=1") {
		t.Errorf("INSERT should have rows=1, got %s", out)
	}
}

// TestE2E_LogUpdateRowsAffected 验证 UPDATE 日志含正确 RowsAffected
func TestE2E_LogUpdateRowsAffected(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (2,'bob',20,NULL)")
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (3,'carol',25,NULL)")
	Users := fusion.Register[LogUser]("users")

	buf, restore := captureFusionLogger(slog.LevelDebug)
	defer restore()

	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	buf.Reset() // 清掉 SELECT 日志，只看 UPDATE

	got.Age.Set(99)
	_ = fusion.Update(Users, wrapped, &got).Where(Users.Proto.ID.Eq(1)).Exec(context.Background())

	out := buf.String()
	if !strings.Contains(out, "op=UPDATE") {
		t.Errorf("should log UPDATE, got %s", out)
	}
	if !strings.Contains(out, "rows=1") {
		t.Errorf("UPDATE 1 row should have rows=1, got %s", out)
	}
}

// TestE2E_LogDeleteRowsAffected 验证 DELETE 日志含正确 RowsAffected（删多行）
func TestE2E_LogDeleteRowsAffected(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (2,'bob',20,NULL)")
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (3,'carol',25,NULL)")
	Users := fusion.Register[LogUser]("users")

	buf, restore := captureFusionLogger(slog.LevelDebug)
	defer restore()

	// 删除 age >= 20 的（alice=30, bob=20, carol=25 全部满足，3 行）
	_ = fusion.Delete(Users, wrapped).Where(Users.Proto.Age.Gte(20)).Exec(context.Background())

	out := buf.String()
	if !strings.Contains(out, "op=DELETE") {
		t.Errorf("should log DELETE, got %s", out)
	}
	if !strings.Contains(out, "rows=3") {
		t.Errorf("DELETE 3 rows should have rows=3, got %s", out)
	}
}

// TestE2E_LogError 验证执行错误记录为 ERROR 级
func TestE2E_LogError(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()

	buf, restore := captureFusionLogger(slog.LevelError) // 只看 Error
	defer restore()

	// 故意查不存在的表（用 Raw）
	var us []LogUser
	_ = fusion.Raw[LogUser](&us, context.Background(), wrapped, "SELECT * FROM nonexistent_table")

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Errorf("should log ERROR, got %s", out)
	}
}

// TestE2E_QueryHook 验证 QueryHook 收到所有操作 + RowsAffected 正确
func TestE2E_QueryHook(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	Users := fusion.Register[LogUser]("users")

	var infos []fusion.QueryInfo
	unreg := fusion.AddQueryHook(func(ctx context.Context, info fusion.QueryInfo) error {
		infos = append(infos, info)
		return nil
	})
	defer unreg()

	// INSERT
	u := &LogUser{}
	u.Name.Set("alice")
	u.Age.Set(30)
	_ = fusion.Insert(Users, wrapped, u).Exec(context.Background())

	// SELECT All
	_, _ = fusion.From(Users, wrapped).All(context.Background())

	// UPDATE
	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(u.ID.Get())).One(context.Background())
	got.Age.Set(40)
	_ = fusion.Update(Users, wrapped, &got).Where(Users.Proto.ID.Eq(u.ID.Get())).Exec(context.Background())

	// DELETE
	_ = fusion.Delete(Users, wrapped).Where(Users.Proto.ID.Eq(u.ID.Get())).Exec(context.Background())

	// 断言：至少 INSERT/SELECT/UPDATE/DELETE 各有记录
	var ops []string
	var rowsByKey map[string]int64 = map[string]int64{}
	for _, info := range infos {
		ops = append(ops, info.Op)
		rowsByKey[info.Op] = info.RowsAffected
	}

	for _, want := range []string{"INSERT", "SELECT", "UPDATE", "DELETE"} {
		found := false
		for _, op := range ops {
			if op == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("QueryHook should receive op %s, got %v", want, ops)
		}
	}

	// RowsAffected 断言
	if rowsByKey["INSERT"] != 1 {
		t.Errorf("INSERT rows got %d, want 1", rowsByKey["INSERT"])
	}
	if rowsByKey["UPDATE"] != 1 {
		t.Errorf("UPDATE rows got %d, want 1", rowsByKey["UPDATE"])
	}
	if rowsByKey["DELETE"] != 1 {
		t.Errorf("DELETE rows got %d, want 1", rowsByKey["DELETE"])
	}
}

// TestE2E_QueryHookUnregister 验证注销后不再触发
func TestE2E_QueryHookUnregister(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	Users := fusion.Register[LogUser]("users")

	count := 0
	unreg := fusion.AddQueryHook(func(ctx context.Context, info fusion.QueryInfo) error {
		count++
		return nil
	})

	_, _ = fusion.From(Users, wrapped).All(context.Background())
	before := count

	unreg()
	_, _ = fusion.From(Users, wrapped).All(context.Background())

	if count != before {
		t.Errorf("after unregister count=%d, before=%d (should not increase)", count, before)
	}
}

// TestE2E_SlowQueryLog 验证慢查询触发 Warn（通过调小阈值）
func TestE2E_SlowQueryLog(t *testing.T) {
	wrapped, raw := setupLogDB(t)
	defer raw.Close()
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (1,'alice',30,'a@e.com')")
	Users := fusion.Register[LogUser]("users")

	origThreshold := 200 * 1000 * 1000 // 记录原始
	_ = origThreshold
	fusion.SetSlowThreshold(0) // 阈值设 0，所有查询都算慢
	defer fusion.SetSlowThreshold(200000000)

	buf, restore := captureFusionLogger(slog.LevelWarn) // 只看 Warn+
	defer restore()

	_, _ = fusion.From(Users, wrapped).All(context.Background())

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("slow query should be WARN, got %s", out)
	}
	if !strings.Contains(out, "slow query") {
		t.Errorf("should contain 'slow query', got %s", out)
	}
}
