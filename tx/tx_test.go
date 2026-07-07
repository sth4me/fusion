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

// capturingBeginner 包装 *sql.DB，记录 BeginTx 收到的 TxOptions。
type capturingBeginner struct {
	*sql.DB
	lastOpts *sql.TxOptions
}

func (c *capturingBeginner) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	c.lastOpts = opts
	return c.DB.BeginTx(ctx, opts)
}

// TestTxWithOpts_Isolation 透传隔离级别/只读到 BeginTx 的 TxOptions。
func TestTxWithOpts_Isolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	cb := &capturingBeginner{DB: db}

	err := TxWithOpts(context.Background(), cb, TxModeSavepoint, &Options{
		TxOptions: &sql.TxOptions{
			Isolation: sql.LevelSerializable,
			ReadOnly:  true,
		},
	}, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if cb.lastOpts == nil {
		t.Fatal("TxOptions not passed through (nil)")
	}
	if cb.lastOpts.Isolation != sql.LevelSerializable {
		t.Errorf("isolation got %v, want Serializable", cb.lastOpts.Isolation)
	}
	if !cb.lastOpts.ReadOnly {
		t.Error("ReadOnly not set")
	}
}

// TestTxWithOpts_NilOptsEqualDefault opts=nil 时 BeginTx 收到 nil（与旧 Tx 行为一致）。
func TestTxWithOpts_NilOptsEqualDefault(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	cb := &capturingBeginner{DB: db}

	err := TxWithOpts(context.Background(), cb, TxModeSavepoint, nil, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	// nil opts → lastOpts 应为 nil（BeginTx(ctx, nil)）
	if cb.lastOpts != nil {
		t.Errorf("nil Options should pass nil TxOptions, got %+v", cb.lastOpts)
	}
}

// TestTxWithOpts_RetryOnDeadlock 验证 fn 首次返回死锁错误时按配置重试，第二次成功。
// 用真实 sqlite（BeginTx 正常），靠 fn 自身返回死锁模拟错误触发重试。
func TestTxWithOpts_RetryOnDeadlock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	calls := 0
	err := TxWithOpts(context.Background(), db, TxModeSavepoint, &Options{
		RetryDeadlocks:  3,
		RetryBaseDelay:  0, // 用默认
		RetryMaxDelay:   0,
	}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			// 模拟 MySQL 死锁错误（字符串含 1213/deadlock）
			return errors.New("Error 1213: Deadlock found when trying to get lock; try restarting transaction")
		}
		// 第二次成功
		runner := FromContext(ctx)
		_, e := runner.ExecContext(ctx, "INSERT INTO accounts (id, balance) VALUES (1, 100)")
		return e
	})
	if err != nil {
		t.Fatalf("should succeed after retry: %v", err)
	}
	if calls != 2 {
		t.Errorf("fn should be called twice (1 deadlock + 1 success), got %d", calls)
	}
	if balance(db, 1) != 100 {
		t.Errorf("balance got %d, want 100", balance(db, 1))
	}
}

// TestTxWithOpts_NoRetryOnNonDeadlock 验证普通错误不重试。
func TestTxWithOpts_NoRetryOnNonDeadlock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	calls := 0
	err := TxWithOpts(context.Background(), db, TxModeSavepoint, &Options{
		RetryDeadlocks: 3,
	}, func(ctx context.Context) error {
		calls++
		return errors.New("some business error")
	})
	if err == nil {
		t.Fatal("should return the business error")
	}
	if calls != 1 {
		t.Errorf("non-deadlock error should not retry, got %d calls", calls)
	}
}

// TestTxWithOpts_RetryExhausted 验证重试次数耗尽后返回错误。
func TestTxWithOpts_RetryExhausted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	calls := 0
	err := TxWithOpts(context.Background(), db, TxModeSavepoint, &Options{
		RetryDeadlocks: 2,
	}, func(ctx context.Context) error {
		calls++
		return errors.New("40P01: deadlock detected")
	})
	if err == nil {
		t.Fatal("should fail after retries exhausted")
	}
	// 1 次首次 + 2 次重试 = 3
	if calls != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries), got %d", calls)
	}
}

// TestIsRetryableTxError 覆盖各类错误字符串识别。
func TestIsRetryableTxError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("40P01: deadlock detected"), true},
		{errors.New("ERROR: could not serialize access (SQLSTATE 40001)"), true},
		{errors.New("Error 1213: Deadlock found"), true},
		{errors.New("Error 1205: Lock wait timeout exceeded"), true},
		{errors.New("try restarting transaction"), true},
		{errors.New("some unrelated error"), false},
		{errors.New("connection refused"), false},
		// H1 回归：裸数字/端口号/耗时不应误判为可重试
		{errors.New("dial tcp: connection :1213 refused"), false},
		{errors.New("query took 1213ms"), false},
		{errors.New("id=1213 not found"), false},
		{errors.New("port 1205 closed"), false},
	}
	for _, c := range cases {
		if got := isRetryableTxError(c.err); got != c.want {
			t.Errorf("isRetryableTxError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestIsRetryableError_RealDriverText 用贴近真实 PG/MySQL 驱动的错误文本验证匹配。
// 这些是驱动在实际死锁时返回的文本形态（pgconn.PgError / mysql.MySQLError 的 Error()）。
func TestIsRetryableError_RealDriverText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// pgx 驱动真实文本：ERROR: deadlock detected (SQLSTATE 40P01)
		{"pg deadlock", errors.New("ERROR: deadlock detected (SQLSTATE 40P01)"), true},
		// pg 序列化失败
		{"pg serial", errors.New("ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)"), true},
		// go-sql-driver/mysql 真实文本：Error 1213: Deadlock found...
		{"mysql deadlock", errors.New("Error 1213: Deadlock found when trying to get lock; try restarting transaction"), true},
		// mysql lock wait timeout
		{"mysql lockwait", errors.New("Error 1205: Lock wait timeout exceeded; try restarting transaction"), true},
		// 非重试able：唯一键冲突、连接拒绝
		{"unique violation", errors.New("Error 1062: Duplicate entry 'x' for key 'uni'"), false},
		{"conn refused", errors.New("dial tcp: connection refused"), false},
		{"port in msg", errors.New("connected to host:3306 but auth failed"), false},
		{"duration in msg", errors.New("query took 1205ms"), false},
	}
	for _, c := range cases {
		if got := IsRetryableError(c.err); got != c.want {
			t.Errorf("%s: IsRetryableError(%q) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
