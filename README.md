# fusion

一个尽可能使用 Go 泛型、减少硬编码、不依赖代码生成的 ORM 库。

## 特性

- **全字段包装**：模型字段用 `orm.Col[T]`，编译期类型安全 + 重命名安全，零字符串列名
- **关联**：belongs_to / has_one / has_many / many_to_many，Preload IN 批量预加载（避免 N+1）
- **灵活 JOIN**：类型安全 ON（`EqCol` 跨表）+ 投影结构体
- **GROUP BY / 聚合 / HAVING / DISTINCT**：Count/Sum/Avg/Min/Max + 聚合排序
- **子查询**：EXISTS / NOT EXISTS / IN 子查询，自动 build，参数并入外层
- **事务**：savepoint 透明嵌套（部分回滚）+ reuse 模式
- **DML**：Insert（单条/批量）/ Update（局部更新 set 标志）/ Delete / Upsert
- **多方言**：PostgreSQL / MySQL / SQLite，方言差异自动抹平
- **日志**：基于 `log/slog`，可桥接 zap/zerolog；QueryHook 拦截 SQL
- **JSON 字段**：`orm.Json[T]` 包装（jsonb / JSON）
- **Raw 兜底**：`orm.Raw[T]` 原始 SQL 复用扫描器

## 快速开始

```go
package main

import (
    "context"
    "database/sql"
    "fmt"
    "log/slog"

    "fusion"
    "fusion/col"
    "fusion/dialect"
    _ "modernc.org/sqlite"
)

type User struct {
    ID    col.Col[int64]
    Name  col.Col[string]
    Age   col.Col[int]
    Email col.Col[*string]  // 可空字段，nil = NULL
}

func main() {
    db, wrapped, err := fusion.Open("sqlite", ":memory:", dialect.SQLiteDialect)
    if err != nil { panic(err) }
    defer db.Close()
    db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER, email TEXT)`)

    // 看所有 SQL（开发期）
    fusion.SetLogger(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

    // 注册模型
    Users := fusion.Register[User]("users")

    // 插入
    u := &User{}
    u.Name.Set("alice")
    u.Age.Set(30)
    fusion.Insert(Users, wrapped, u).Exec(context.Background())
    fmt.Println("inserted ID:", u.ID.Get())  // 自增 ID 已回填

    // 类型安全查询
    users, _ := fusion.From(Users, wrapped).
        Where(Users.Proto.Age.Gt(18).And(Users.Proto.Name.Like("a%"))).
        OrderBy(Users.Proto.Age.Desc()).
        Limit(10).
        All(context.Background())

    // 局部更新（只改 Set 过的字段）
    users[0].Age.Set(31)
    fusion.Update(Users, wrapped, &users[0]).Exec(context.Background())
    // 无 Where 时自动按主键更新

    fmt.Println(users[0].Name.Get(), users[0].Age.Get())  // 显式 Get
}
```

## 关联

```go
type Post struct {
    ID   col.Col[int64]
    UID  col.Col[int64]
    Title col.Col[string]
}

// 注册关联（回调式取字段，类型安全零字符串）
var Posts = fusion.Register[Post]("posts")
fusion.HasMany(
    func(u *User) any { return &u.Posts },
    func(p *Post) any { return &p.UID },
    func(u *User) any { return &u.ID },
)

// Preload（默认不查关联，显式才查）
users, _ := fusion.From(Users, wrapped).
    Preload("Posts").
    Where(Users.Proto.ID.Eq(1)).
    All(context.Background())
posts, _ := users[0].Posts.All()  // 未 Preload 返回 ErrNotLoaded
```

## 灵活 JOIN + 聚合

```go
type Dept struct { ID col.Col[int64]; Name col.Col[string] }
var Depts = fusion.Register[Dept]("depts")

type UserDeptView struct {
    UserName string  `db:"user_name"`
    DeptName *string `db:"dept_name"`  // LEFT JOIN 可能 NULL
}
fusion.Register[UserDeptView]("")

// 类型安全 JOIN
var view []UserDeptView
fusion.From(Users, wrapped).As("u").
    Join(fusion.InnerJoin, Depts, "d", Users.Proto.DeptID.EqCol(Depts.Proto.ID)).
    Select(Users.Proto.Name.As("user_name"), Depts.Proto.Name.As("dept_name")).
    AllInto(context.Background(), &view)

// GROUP BY + 聚合
type DeptStat struct { DeptID int64 `db:"dept_id"`; Cnt int64 `db:"cnt"` }
fusion.Register[DeptStat]("")
var stats []DeptStat
fusion.From(Users, wrapped).As("u").
    Select(Users.Proto.DeptID.As("dept_id"), fusion.Count[int64]().As("cnt")).
    GroupBy(Users.Proto.DeptID.GroupBy()).
    Having(col.CountGt(1)).
    AllInto(context.Background(), &stats)
```

## 子查询

```go
// 查有文章的用户（EXISTS 相关子查询，自动 build，零字符串）
subQ := fusion.From(Posts, wrapped).Where(Posts.Proto.UID.EqCol(Users.Proto.ID))
users, _ := fusion.From(Users, wrapped).
    Where(fusion.Exists(subQ)).
    All(context.Background())
```

## 事务

```go
fusion.Tx(context.Background(), db, func(ctx context.Context) error {
    // 在此 ctx 内的所有操作自动走同一事务
    fusion.Insert(Users, wrapped, &u).Exec(ctx)
    if err := doSomething(); err != nil {
        return err  // 返回 error 自动回滚
    }
    return nil  // 返回 nil 自动提交
})
// 嵌套事务用 savepoint（默认），支持部分回滚
```

## 日志

```go
// 用 slog 看所有 SQL
fusion.SetLogger(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
})))

// 桥接 zap: fusion.SetLogger(slog.New(zapslog.New(zapLogger.Core())))

// 拦截所有查询做审计
fusion.AddQueryHook(func(ctx context.Context, info fusion.QueryInfo) error {
    auditLog(info.Op, info.SQL, info.Duration, info.RowsAffected)
    return nil
})
```

## 设计文档

详见 [docs/DESIGN.md](docs/DESIGN.md)，包含 9 项核心设计决策的完整推理。

## 支持的 Go 版本

Go 1.26+（Go 1.27 泛型方法增强为远期可选项）。
