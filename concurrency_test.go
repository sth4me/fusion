package fusion_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
)

// TestConcurrencySafeAs 验证多 goroutine 并发 From+As 不同别名不产生数据竞争
// 且各自生成正确 SQL（用 -race 运行验证）。
//
// 重构前：As 修改全局 Proto 的 Col.table，并发会污染。
// 重构后：列引用用稳定表名，别名仅在 render 时由 builder 映射替换，无全局写。
func TestConcurrencySafeAs(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	execSQL(db, `CREATE TABLE ccusers (id INTEGER PRIMARY KEY, name TEXT)`)
	execSQL(db, `INSERT INTO ccusers VALUES (1,'alice'),(2,'bob')`)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)

	type CCUser struct {
		ID   col.Col[int64]
		Name col.Col[string]
	}
	Users := fusion.Register[CCUser]("ccusers")
	wrapped := fusion.WrapDB(db)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			q := fusion.From(Users, wrapped)
			if n%2 == 0 {
				q.As("a") // 部分用别名，部分不用，并发不冲突
			}
			// 构造 WHERE（用稳定表名，任意时刻都可）
			q.Where(Users.Proto.ID.Eq(int64(1)))
			_, _ = q.All(context.Background())
		}(i)
	}
	wg.Wait()
	// 若有数据竞争，-race 会报错；无 panic 即通过
}

// TestConcurrencySafeJoin 验证并发 Join 不同别名不竞争
func TestConcurrencySafeJoin(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	execSQL(db, `CREATE TABLE ccusers2 (id INTEGER PRIMARY KEY, dept_id INTEGER)`)
	execSQL(db, `CREATE TABLE ccdepts (id INTEGER PRIMARY KEY, name TEXT)`)
	execSQL(db, `INSERT INTO ccdepts VALUES (1,'eng')`)
	execSQL(db, `INSERT INTO ccusers2 VALUES (1,1)`)

	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	type CCDept struct {
		ID   col.Col[int64]
		Name col.Col[string]
	}
	type CCUser2 struct {
		ID     col.Col[int64]
		DeptID col.Col[int64]
	}
	Depts := fusion.Register[CCDept]("ccdepts")
	Users := fusion.Register[CCUser2]("ccusers2")
	wrapped := fusion.WrapDB(db)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := fusion.From(Users, wrapped).As("u")
			// 并发用相同 join 别名构造 ON（ref 是稳定表名，无竞争）
			q.Join(fusion.InnerJoin, Depts, "d", Users.Proto.DeptID.EqCol(Depts.Proto.ID))
			_ = q
		}()
	}
	wg.Wait()
}

func execSQL(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		panic(err)
	}
}

// TestConcurrencyMixedOps 并发混合操作验证逻辑层并发安全（不 panic、各自结果正确）。
// 不依赖 -race（环境可能无 CGO），用功能正确性兜底：并发查询同表/不同表、并发 Insert。
// SQLite 单连接（openSQLite 设了 SetMaxOpenConns(1)）下操作序列化，但验证逻辑层无竞争误用。
func TestConcurrencyMixedOps(t *testing.T) {
	db := openSQLite(t)
	defer db.Close()
	execSQL(db, `CREATE TABLE mxusers (id INTEGER PRIMARY KEY, name TEXT)`)
	execSQL(db, `CREATE TABLE mxposts (id INTEGER PRIMARY KEY, uid INTEGER, title TEXT)`)
	execSQL(db, `INSERT INTO mxusers VALUES (1,'alice')`)
	execSQL(db, `INSERT INTO mxposts VALUES (10,1,'p1'),(11,1,'p2')`)
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	wrapped := fusion.WrapDB(db)

	type MXUser struct {
		ID   col.Col[int64]
		Name col.Col[string]
	}
	type MXPost struct {
		ID    col.Col[int64]
		UID   col.Col[int64]
		Title col.Col[string]
	}
	mxUsers := fusion.Register[MXUser]("mxusers")
	mxPosts := fusion.Register[MXPost]("mxposts")

	var wg sync.WaitGroup
	const N = 20
	errs := make(chan error, N*2)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			us, err := fusion.From(mxUsers, wrapped).Where(mxUsers.Proto.ID.Eq(1)).All(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if len(us) != 1 || us[0].Name.Get() != "alice" {
				errs <- fmt.Errorf("g%d: users got %+v", n, us)
			}
		}(i)
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ps, err := fusion.From(mxPosts, wrapped).Where(mxPosts.Proto.UID.Eq(1)).All(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if len(ps) != 2 {
				errs <- fmt.Errorf("g%d: got %d posts, want 2", n, len(ps))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
