package fusion_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
)

// THUser 用于事务+钩子测试
type THUser struct {
	ID    col.Col[int64]
	Name  col.Col[string]
	Age   col.Col[int]
	Email col.Col[*string]
}

func setupTHDB(t *testing.T) (fusion.DB, *sql.DB) {
	db := openSQLite(t)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	Users := fusion.Register[THUser]("users")
	_ = Users
	return fusion.WrapDB(db), db
}

// TestE2E_TxWithDML 验证：DML 在 orm.Tx 回调中通过 ctx 自动走事务
func TestE2E_TxWithDML(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	err := fusion.Tx(context.Background(), raw, func(ctx context.Context) error {
		u := &THUser{}
		u.Name.Set("alice")
		u.Age.Set(30)
		// wrapped 是 WrapDB 的结果，ctx 内有事务 → 自动走事务
		return fusion.Insert(Users, wrapped, u).Exec(ctx)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	// 提交后数据应在
	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.Name.Eq("alice")).One(context.Background())
	if got.Age.Get() != 30 {
		t.Errorf("after commit age got %d", got.Age.Get())
	}
}

// TestE2E_TxRollback 验证事务回滚
func TestE2E_TxRollback(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	err := fusion.Tx(context.Background(), raw, func(ctx context.Context) error {
		u := &THUser{}
		u.Name.Set("bob")
		u.Age.Set(20)
		if err := fusion.Insert(Users, wrapped, u).Exec(ctx); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("should error")
	}
	// 回滚后数据不应存在
	all, _ := fusion.From(Users, wrapped).All(context.Background())
	if len(all) != 0 {
		t.Errorf("got %d users after rollback, want 0", len(all))
	}
}

// TestE2E_HookBeforeCreate 验证 BeforeCreate 钩子触发且能修改实体
func TestE2E_HookBeforeCreate(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	// 钩子：插入前把 Name 转大写
	unreg := fusion.OnHook((*THUser)(nil), fusion.BeforeCreate, func(ctx context.Context, target any) error {
		u := target.(*THUser)
		// 在钩子内修改字段（大写化）
		u.Name.Set("ALICE")
		return nil
	})
	defer unreg()

	u := &THUser{}
	u.Name.Set("alice")
	u.Age.Set(30)
	if err := fusion.Insert(Users, wrapped, u).Exec(context.Background()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(u.ID.Get())).One(context.Background())
	if got.Name.Get() != "ALICE" {
		t.Errorf("name got %q, want ALICE (hook modified)", got.Name.Get())
	}
}

// TestE2E_HookAbortOnError 验证 BeforeCreate 返回 error 时中止插入
func TestE2E_HookAbortOnError(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	unreg := fusion.OnHook((*THUser)(nil), fusion.BeforeCreate, func(ctx context.Context, target any) error {
		u := target.(*THUser)
		if u.Age.Get() < 18 {
			return errors.New("underage not allowed")
		}
		return nil
	})
	defer unreg()

	u := &THUser{}
	u.Name.Set("teen")
	u.Age.Set(15)
	err := fusion.Insert(Users, wrapped, u).Exec(context.Background())
	if err == nil {
		t.Fatal("insert should be aborted by hook")
	}
	// 确认未插入
	all, _ := fusion.From(Users, wrapped).All(context.Background())
	if len(all) != 0 {
		t.Errorf("got %d users, want 0 (aborted)", len(all))
	}
}

// TestE2E_HookAfterUpdate 验证 AfterUpdate 触发
func TestE2E_HookAfterUpdate(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	execInsert(raw, "INSERT INTO users (id,name,age,email) VALUES (1,'old',30,NULL)")
	Users := fusion.Register[THUser]("users")

	fired := false
	unreg := fusion.OnHook((*THUser)(nil), fusion.AfterUpdate, func(ctx context.Context, target any) error {
		fired = true
		return nil
	})
	defer unreg()

	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.ID.Eq(1)).One(context.Background())
	got.Name.Set("new")
	fusion.Update(Users, wrapped, &got).Where(Users.Proto.ID.Eq(1)).Exec(context.Background())
	if !fired {
		t.Error("AfterUpdate hook should fire")
	}
}

// TestE2E_TxSavepointPartialRollback 端到端验证 savepoint 部分回滚
func TestE2E_TxSavepointPartialRollback(t *testing.T) {
	fusion.SetDefaultTxMode(fusion.TxModeSavepoint)
	defer fusion.SetDefaultTxMode(fusion.TxModeSavepoint)
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	err := fusion.Tx(context.Background(), raw, func(ctx context.Context) error {
		// 成功插入 alice
		a := &THUser{}
		a.Name.Set("alice")
		a.Age.Set(30)
		fusion.Insert(Users, wrapped, a).Exec(ctx)

		// 内层 savepoint：插入 bob 后失败 → 只回滚内层
		innerErr := fusion.Tx(ctx, raw, func(ctx context.Context) error {
			b := &THUser{}
			b.Name.Set("bob")
			b.Age.Set(20)
			fusion.Insert(Users, wrapped, b).Exec(ctx)
			return errors.New("inner fail")
		})
		if innerErr == nil {
			return errors.New("inner should fail")
		}

		// 外层继续：插入 carol
		c := &THUser{}
		c.Name.Set("carol")
		c.Age.Set(25)
		fusion.Insert(Users, wrapped, c).Exec(ctx)
		return nil
	})
	if err != nil {
		t.Fatalf("outer: %v", err)
	}

	all, _ := fusion.From(Users, wrapped).All(context.Background())
	// alice + carol 提交，bob 被 savepoint 回滚
	if len(all) != 2 {
		names := []string{}
		for _, u := range all {
			names = append(names, u.Name.Get())
		}
		t.Fatalf("got %d users %v, want 2 (alice, carol)", len(all), names)
	}
	for _, u := range all {
		if u.Name.Get() == "bob" {
			t.Error("bob should be rolled back by savepoint")
		}
	}
}

// TestE2E_TxWithOpts 端到端验证 fusion.TxWith 的选项 API（隔离级别 + 重试）。
func TestE2E_TxWithOpts(t *testing.T) {
	wrapped, raw := setupTHDB(t)
	defer raw.Close()
	Users := fusion.Register[THUser]("users")

	calls := 0
	err := fusion.TxWith(context.Background(), raw,
		func(ctx context.Context) error {
			calls++
			if calls == 1 {
				// 模拟死锁，触发重试
				return errors.New("Error 1213: Deadlock; try restarting transaction")
			}
			u := &THUser{}
			u.Name.Set("alice")
			u.Age.Set(30)
			return fusion.Insert(Users, wrapped, u).Exec(ctx)
		},
		// 可同时传多个选项
		fusion.WithIsolation(sql.LevelSerializable),
		fusion.WithRetry(3, 0, 0),
	)
	if err != nil {
		t.Fatalf("TxWith: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 deadlock + 1 success), got %d", calls)
	}
	got, _ := fusion.From(Users, wrapped).Where(Users.Proto.Name.Eq("alice")).One(context.Background())
	if got.Age.Get() != 30 {
		t.Errorf("after retry age got %d", got.Age.Get())
	}
}
