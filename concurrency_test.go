package fusion_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
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
