// Package main 演示 fusion ORM 的典型项目结构。
//
// 结构：
//   - model/：模型类型 + 全局 Table 变量 + RegisterAll（可被任意业务层 import）
//   - main.go：连接数据库、调用 model.RegisterAll()、演示查询
//
// 运行：cd examples/crud && go run .
// 需要内存 SQLite（已在 go.mod 引入 modernc.org/sqlite）。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/sth4me/fusion/examples/crud/model"

	"github.com/sth4me/fusion"
	"github.com/sth4me/fusion/dialect"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 初始化方言 + 建表（fusion 不做正向迁移，手工建表）
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	mustExec(db, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, age INTEGER NOT NULL, email TEXT)`)
	mustExec(db, `INSERT INTO users (id, name, age, email) VALUES (1,'alice',30,'a@e.com'),(2,'bob',17,NULL),(3,'carol',25,'c@e.com')`)

	// 注册模型（main 启动时调一次，填充 model.Users 等全局 Table 变量）。
	// 之后业务层通过 model.Users 引用，无需再传 db/方言——这些在 From/Insert 时传。
	model.RegisterAll()

	ctx := context.Background()

	// 1) 查询全部（model.Users 是 *meta.Table[User]，类型安全）
	fmt.Println("== 全部用户 ==")
	all, _ := fusion.From(model.Users, db).All(ctx)
	for _, u := range all {
		printUser(u)
	}

	// 2) 类型安全 WHERE：age > 18
	fmt.Println("\n== 成年用户 (age > 18) ==")
	adults, _ := fusion.From(model.Users, db).
		Where(model.Users.Proto.Age.Gt(18)).
		All(ctx)
	for _, u := range adults {
		printUser(u)
	}

	// 3) 复杂表达式：(name='alice' AND age>18) OR name='bob'
	fmt.Println("\n== 复杂查询 ==")
	u := model.Users.Proto
	mixed, _ := fusion.From(model.Users, db).
		Where(
			u.Name.Eq("alice").And(u.Age.Gt(18)).
				Or(u.Name.Eq("bob")),
		).
		All(ctx)
	for _, x := range mixed {
		printUser(x)
	}

	// 4) 排序 + 分页
	fmt.Println("\n== 按 age 降序，取前 2 ==")
	top2, _ := fusion.From(model.Users, db).
		OrderBy(model.Users.Proto.Age.Desc()).
		Limit(2).
		All(ctx)
	for _, x := range top2 {
		printUser(x)
	}

	// 5) 单条查询
	fmt.Println("\n== 单条：bob ==")
	bob, _ := fusion.From(model.Users, db).Where(model.Users.Proto.Name.Eq("bob")).One(ctx)
	printUser(bob)

	// 6) JSON 透明序列化
	fmt.Println("\n== JSON（透明，输出原生形态）==")
	fmt.Println(jsonStr(bob))
}

func printUser(u model.User) {
	email := "NULL"
	if e := u.Email.Get(); e != nil {
		email = *e
	}
	fmt.Printf("  id=%d name=%s age=%d email=%s\n", u.ID.Get(), u.Name.Get(), u.Age.Get(), email)
}

func mustExec(db *sql.DB, q string) {
	if _, err := db.Exec(q); err != nil {
		log.Fatal(err)
	}
}

func jsonStr(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		return fmt.Sprintf("<err: %v>", err)
	}
	return string(b)
}
