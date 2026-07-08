# fusion

一个尽可能使用 Go 泛型、减少硬编码、不依赖代码生成的 ORM 库。

## 特性

- **全字段包装**：模型字段用 `orm.Col[T]`，编译期类型安全 + 重命名安全，零字符串列名；支持**复合主键**（多 `db:"pk"`）
- **关联**：belongs_to / has_one / has_many / many_to_many，Preload IN 批量预加载（避免 N+1），
  支持**点号嵌套**（`Preload("Posts.Comments")`）
- **反向迁移**：读数据库 schema 构建运行时元数据（`LoadSchema`），**Bind 校验模型漂移**，
  **从外键自动注册关联**（手动优先，无外键也能用）
- **Engine**：多库场景用 `fusion.New(db, opts...)` 创建独立 Engine（各自方言/logger/事务选项，互不干扰）；
  单库用全局 API（默认 Engine 语法糖）一样方便
- **灵活 JOIN**：类型安全 ON（`EqCol` 跨表）+ 投影结构体
- **GROUP BY / 聚合 / HAVING / DISTINCT**：Count/Sum/Avg/Min/Max + 聚合排序
- **UNION / INTERSECT / EXCEPT**：集合复合查询，尾部 ORDER/LIMIT 作用于整体
- **CTE（WITH）**：含**递归**（树形/层级查询），CTE 体参数并入外层
- **窗口函数**：`RowNumber/Rank/DenseRank/Lag/Lead` + `Over(partition, order)`（排名/累计/滑动窗口）
- **子查询**：EXISTS / NOT EXISTS / IN 子查询，自动 build，参数并入外层
- **事务**：savepoint 透明嵌套（部分回滚）+ reuse 模式；**隔离级别透传** + **死锁自动重试**
- **DML**：Insert（单条/批量）/ Update（局部更新 set 标志，`Col.Reset()` 撤销 dirty）/ Delete / Upsert
- **多方言**：PostgreSQL / MySQL / SQLite，方言差异自动抹平
- **日志**：基于 `log/slog`，可桥接 zap/zerolog；QueryHook 拦截 SQL；**敏感字段自动脱敏**（password/token 等列的值在日志中替换为 `***`）
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

## 推荐项目结构

真实项目里，模型类型和它的 `*Table` 变量建议放专门的 `model` 包，`main` 只负责连接和调用注册。
这样业务层（service/handler）通过 `import model` 拿到全局 Table 变量，依赖单向、无循环。
完整示例见 `examples/crud`。

```
yourapp/
├── model/
│   ├── user.go        // type User struct { ... }
│   └── registry.go    // var Users *Table + func RegisterAll()
├── service/
│   └── user_service.go// import model; fusion.From(model.Users, db)
└── main.go            // model.RegisterAll() 启动时调一次
```

```go
// model/registry.go
package model

import "github.com/sth4me/fusion"

var (
    Users *fusion.Table  // 实际类型 *meta.Table[User]；初始 nil，RegisterAll 后赋值
    Posts *fusion.Table
)

// RegisterAll 注册所有模型与关联，main 启动时调用一次。
// 不放 init()：避免 import 副作用（任何 import model 都触发注册）；
// 集中注册便于控制顺序（先模型后关联）和按环境传表名。
func RegisterAll() {
    Users = fusion.Register[User]("users")
    Posts = fusion.Register[Post]("posts")
    // 关联注册在此：fusion.HasMany(...)。注意先 Register 类型，再注册关联。
}
```

```go
// service/user_service.go
package service

import "yourapp/model"

func GetUser(ctx context.Context, id int64) (*model.User, error) {
    return fusion.From(model.Users, db).Where(model.Users.Proto.ID.Eq(id)).One(ctx)
}
```

```go
// main.go
func main() {
    db, _ := fusion.Open("sqlite", ":memory:", dialect.SQLiteDialect)
    model.RegisterAll()  // 全局 Table 变量就绪
    // ... 业务层用 model.Users 查询
}
```

依赖方向单向：`service → model → fusion`，`model` 是叶子节点，任何上层 import 它拿 Table 变量，不会循环。

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

// Preload（默认不查关联，显式才查）；支持点号嵌套（每段 IN 批量）
users, _ := fusion.From(Users, wrapped).
    Preload("Posts").               // 单层
    Preload("Posts.Comments").      // 嵌套：User→Posts→Comments
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

隔离级别与死锁重试（函数式选项，仅顶层事务生效）：

```go
fusion.TxWith(ctx, db,
    func(ctx context.Context) error { /* ... */ },
    fusion.WithIsolation(sql.LevelSerializable),                                   // 隔离级别
    fusion.WithReadOnly(),                                                          // 只读
    fusion.WithRetry(3, 5*time.Millisecond, 100*time.Millisecond),                 // 死锁指数退避重试
)
// 重试仅针对可重试错误（PG 40P01/40001、MySQL 1213/1205 等）；fn 必须幂等。
```

## Engine（多库）

单库用全局 API（最易用）；多库用 `fusion.New` 创建独立 Engine，各自方言/logger/事务选项互不干扰：

```go
// 多库：PG 主库 + SQLite 缓存库
pgEngine := fusion.New(pgDB, fusion.WithDialect(dialect.PostgresDialect))
cacheEngine := fusion.New(cacheDB, fusion.WithDialect(dialect.SQLiteDialect))

// 各自查询（Go 1.26 不支持泛型方法，故 Engine 入口为 E 前缀顶层函数）
pgEngine // → fusion.EFrom(pgEngine, Users).Where(...).All(ctx)
cacheEngine // → fusion.EFrom(cacheEngine, CacheItems).Where(...).All(ctx)

// 各自 logger / 慢阈值 / 事务默认
auditEngine := fusion.New(db,
    fusion.WithDialect(dialect.PostgresDialect),
    fusion.WithLogger(auditLogger),
    fusion.WithSlowThreshold(500*time.Millisecond),
    fusion.WithTxIsolation(sql.LevelSerializable),
)
// 事务：fusion.ETx(auditEngine, ctx, func(ctx) error { ... })
// per-Engine logger：查询时传 engine.Ctx(ctx) 把 logger 覆盖挂到 ctx
fusion.EFrom(auditEngine, Users).Where(...).All(auditEngine.Ctx(ctx))
```

## 集合查询（UNION/CTE/窗口）

```go
// UNION（尾部 ORDER BY 作用于整体）
q1 := fusion.From(Active, db).Where(Active.Proto.State.Eq("on"))
q2 := fusion.From(Archived, db).Where(Archived.Proto.State.Eq("off"))
all, _ := fusion.Union(q1, q2).OrderBy(/* ... */).All(ctx)
// 也支持 fusion.UnionAll / Intersect / Except

// CTE / WITH（递归：评论楼中楼）
fusion.From(Comments, db).
    With("tree", `SELECT ... UNION ALL SELECT ... JOIN tree ON ...`,
        []any{rootID}, true /*recursive*/).
    Where(Comments.Proto.ID.Eq(1)).All(ctx)
// 递归 CTE 引用（FROM tree）走 fusion.Raw 最干净（builder FROM 固定模型表）

// 窗口函数（按部门排名）
fusion.From(Emps, db).Select(
    Emps.Proto.ID.As("id"),
    fusion.RowNumber().Over([]string{"dept"}, []string{"salary DESC"}).As("rn"),
).AllInto(ctx, &view)
// 也支持 fusion.Rank / DenseRank / Lag(col) / Lead(col) / Sum(col).Over(...)
```

## 敏感字段脱敏

日志中敏感列的参数值自动替换为 `***`（默认含 password/token/api_key 等常见名）：

```go
fusion.AddSensitiveColumn("ssn", "credit_card")            // 追加
fusion.SetSensitiveColumns([]string{"password", "token"})   // 覆盖
fusion.SetRedactionEnabled(false)                           // 关闭（默认开）
// 覆盖 WHERE/SET 的 col=? / col IN(...) / col BETWEEN / col LIKE 模式；
// INSERT VALUES 的列映射保守不脱敏（用户可用 AddQueryHook 自行处理）。
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

> 代码生成 CLI（`fusion/cmd/fusion-gen`）为**远期可选项**：反向迁移的运行时路线
> （`LoadSchema` + `Bind` + `AutoRegisterRelations`）已覆盖核心能力，CLI 仅在需要
> 静态模型/DAO 脚手架时另作独立工具，不进核心库。

## 反向迁移（DB → 元数据）

fusion 不生成 `.go` 源码、不做正向迁移（Model → DDL）。原因是 `Col[T]` 不携带 SQL
类型/长度/约束信息，正向生成有损且需大量人介入。**反向路线则确定且互补**：数据库是真理，
读它构建运行时 `schema.Catalog`，与 `meta.ModelMeta`（反射侧）拼出完整图景。

三个核心能力：

```go
// 1) 内省数据库 schema → Catalog（缓存列/主键/外键/索引，三方言）
cat, _ := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "users", "posts")

// 2) Bind：校验模型 vs 数据库是否漂移（启动期 fail-fast）
fusion.MustBind(cat, Users)  // 列缺失/多余/主键不一致 → panic 报具体差异

// 3) AutoRegisterRelations：从外键自动注册 belongs_to + has_many（手动优先）
fusion.AutoRegisterRelations(cat)
// 等价于：扫到 posts.user_id → users.id，自动注册
//   relation.BelongsTo(Post.Dept, ...) + relation.HasMany(User.Posts, ...)
// 之后可直接 Preload，无需手写任何 HasMany/BelongsTo。
```

**关联注册双轨制（重要）**：fusion 同时支持手写和外键自动发现，二者并存：

- **有外键**：`AutoRegisterRelations` 扫外键自动注册，零样板代码。
- **无外键**（分库分表、性能、应用层管完整性的常见实践）：函数 no-op，完全靠手写
  `fusion.HasMany/BelongsTo/...`，和之前一样。
- **手动优先**：已手写注册的关联**不被**自动注册覆盖。

命名约定（自动注册匹配字段名）：
- belongs_to 字段：FK 列去 `_id` 后缀 PascalCase（`dept_id` → `Dept`），须为 `rel.Rel[T]`。
- has_many 字段：子模型类型名复数化（`Post` → `Posts`），须为 `rel.RelMany[T]`。
- 约定不符 / 复合外键 / 子模型未注册 → 跳过并 Debug log（用户可手写补充）。

完整示例见 `examples/reverse`。

## 支持的 Go 版本

Go 1.26+（Go 1.27 泛型方法增强为远期可选项）。

## License

MIT（见 [LICENSE](LICENSE)）。
