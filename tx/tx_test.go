package tx

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func balance(db *sql.DB, id int) int {
	var b int
	_ = db.QueryRow("SELECT balance FROM accounts WHERE id=?", id).Scan(&b)
	return b
}

// TestTx_Commit 验证顶层事务提交
func TestTx_Commit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := Tx(context.Background(), db, TxModeSavepoint, func(ctx context.Context) error {
		runner := FromContext(ctx)
		if runner == nil {
			return errors.New("no runner in tx")
		}
		_, err := runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if balance(db, 1) != 100 {
		t.Errorf("balance got %d, want 100 (committed)", balance(db, 1))
	}
}

// TestTx_Rollback 验证顶层事务回滚
func TestTx_Rollback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := Tx(context.Background(), db, TxModeSavepoint, func(ctx context.Context) error {
		runner := FromContext(ctx)
		_, err := runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")
		if err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("should return error")
	}
	// 回滚后数据不应存在
	if balance(db, 1) != 0 {
		t.Errorf("balance got %d, want 0 (rolled back)", balance(db, 1))
	}
}

// TestTx_SavepointPartialRollback 验证 savepoint 部分回滚
func TestTx_SavepointPartialRollback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := Tx(context.Background(), db, TxModeSavepoint, func(ctx context.Context) error {
		runner := FromContext(ctx)
		// 成功插入
		runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")

		// 内层 savepoint：插入后返回错误 → 只回滚内层
		innerErr := Tx(ctx, db, TxModeSavepoint, func(ctx context.Context) error {
			r := FromContext(ctx)
			r.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (2, 200)")
			return errors.New("inner fail")
		})
		if innerErr == nil {
			return errors.New("inner should fail")
		}
		// 内层已回滚，外层继续：插入 id=3
		runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (3, 300)")
		return nil // 外层提交
	})
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	// id=1 提交，id=2 被内层 savepoint 回滚，id=3 提交
	if balance(db, 1) != 100 {
		t.Errorf("id=1 got %d, want 100", balance(db, 1))
	}
	if balance(db, 2) != 0 {
		t.Errorf("id=2 got %d, want 0 (savepoint rolled back)", balance(db, 2))
	}
	if balance(db, 3) != 300 {
		t.Errorf("id=3 got %d, want 300", balance(db, 3))
	}
}

// TestTx_ReuseNoPartialRollback 验证 reuse 模式不部分回滚
func TestTx_ReuseNoPartialRollback(t *testing.T) {
	SetDefaultMode(TxModeReuse)
	defer SetDefaultMode(TxModeSavepoint)

	db := openTestDB(t)
	defer db.Close()

	err := Tx(context.Background(), db, TxModeReuse, func(ctx context.Context) error {
		runner := FromContext(ctx)
		runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")

		// 内层 reuse：插入但返回错误
		_ = Tx(ctx, db, TxModeReuse, func(ctx context.Context) error {
			r := FromContext(ctx)
			r.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (2, 200)")
			return errors.New("inner fail")
		})
		// reuse 模式：内层不回滚（no-op），数据保留
		// 外层也不 return err → 提交，id=1 和 id=2 都提交
		return nil
	})
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	if balance(db, 1) != 100 {
		t.Errorf("id=1 got %d", balance(db, 1))
	}
	// reuse 模式下内层操作未被回滚
	if balance(db, 2) != 200 {
		t.Errorf("id=2 got %d, want 200 (reuse: no partial rollback)", balance(db, 2))
	}
}

// TestTx_ReuseOuterRollbackAll 验证 reuse 模式外层回滚则全部回滚
func TestTx_ReuseOuterRollbackAll(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	err := Tx(context.Background(), db, TxModeReuse, func(ctx context.Context) error {
		runner := FromContext(ctx)
		runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")
		_ = Tx(ctx, db, TxModeReuse, func(ctx context.Context) error {
			FromContext(ctx).ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (2, 200)")
			return nil
		})
		return errors.New("outer fail") // 外层失败 → 整体回滚
	})
	if err == nil {
		t.Fatal("should error")
	}
	if balance(db, 1) != 0 || balance(db, 2) != 0 {
		t.Error("reuse: outer rollback should roll back all")
	}
}

// TestTx_DefaultMode 验证默认模式生效
func TestTx_DefaultMode(t *testing.T) {
	SetDefaultMode(TxModeReuse)
	defer SetDefaultMode(TxModeSavepoint)
	if DefaultMode() != TxModeReuse {
		t.Error("default mode should be reuse")
	}
}
