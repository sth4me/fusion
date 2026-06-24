# fusion — 泛型 ORM 设计文档

> 状态：设计中（阶段 0 实施前）
> 最后更新：2026-06-23
> Go 版本：1.26（当前可用），1.27（2026.8，条件泛型方法，增强项）

---

## 一、项目目标与约束

### 目标
- 尽可能使用 Go 泛型，减少硬编码
- 尽量不使用代码生成
- 最终兜底允许 Raw SQL
- 支持 PostgreSQL / MySQL / SQLite（抽象需满足三者，未来可扩充）

### 硬约束（Go 泛型方法边界）
- **Go 1.26**：只有类型可以有类型参数，方法不行
- **Go 1.27**：具体类型可以有泛型方法，但 **interface 仍不能声明泛型方法**。这意味着承载泛型方法的 builder 必须是具体类型，不能放进 interface 定义

---

## 二、核心设计决策（经多轮权衡确定）

### 决策 1：字段方案 — 全字段包装（X 方案）

**结论**：所有字段用 `orm.Col[T]`，关联用 `orm.Rel[T]`/`orm.RelMany[T]`。字段即带元数据的描述符。

#### 决策推理过程

经过对四个方案的对比，最终选择全字段包装。核心论据：

| 维度 | 全包装 X | 只包装关联 Y | 全普通结构体 Z | Column内嵌(早期方案) |
|---|---|---|---|---|
| 模型负担 | 字段全包装 | 普通+关联混合 | 纯结构体 | 字段全包装 |
| 读写税 | Get/Set（赋值/比较/运算/传参） | 仅关联 Get | 零 | Get/Set |
| 关联 nil 二义 | 根治（状态入类型） | 根治 | 靠侧表+Loaded() | 根治 |
| 忘 Preload panic | 根治（明确错误） | 根治 | Debug 警告 | 根治 |
| 值类型空壳陷阱 | 根治（结构消除） | 根治 | 靠纪律强制指针 | 根治 |
| 字段引用样板 | 显著简化（字段即描述符） | 关联简化 | var u/Ref[T]() | 显著简化 |
| 关联声明隐式 | 根治（类型可见） | 根治 | 翻注册文件 | 根治 |
| 元数据基础设施 | 字段即元数据 | 混合 | 指针→偏移→查表+侧表 | 字段即元数据 |
| JSON/sql/序列化 | 全自动透明 | 普通+关联透明 | 无需 | 全自动透明 |

#### 关键认知修正
- 早期否定 Column[T] 的论证"主流 ORM 都不用"是诉诸权威，无效。真实原因是那些 ORM 诞生在泛型之前。
- Column[T] 的弱点里，**JSON/sql/序列化/打印可通过实现标准接口全自动透明**；真正不可弥补的只有 **Get/Set**（赋值/比较/运算/传参）。
- Get/Set 本质是把 Java/C# 的 getter/setter 显式化，不是额外负担。
- 全包装用 Get/Set 一个代价，换掉了 Z 方案的 4 个痛点 + 2 块复杂基础设施（侧表、偏移查表），工程上更干净。

#### X 方案根除的痛点
1. 关联 nil 二义（未加载 vs 无关联）→ 状态入类型，`Loaded()`/`IsNil()` 明确区分
2. 忘 Preload 直接用关联 → `Get()` 返回 `ErrNotLoaded`，明确错误而非 panic
3. 值类型空壳陷阱 → 包装类型让"关联必须是包装"成为唯一可能
4. 字段引用样板 → 字段即描述符，无需 `var u User` / `orm.Ref[T]()`
5. 关联声明隐式 → `orm.Rel[T]` 类型签名即可见，无需翻注册文件

#### X 方案消灭的基础设施（Z 方案需要）
- 指针→偏移→元数据查表
- 侧表 `map[偏移]bool` 管理加载状态

### 决策 2：WHERE — 仅接受表达式

**结论**：`Where` 只接受 `Expr`，不提供"多个 Where 隐式 AND"。

**推理**：`Where(c1).Where(c2)` 链式 AND 的陷阱是 OR 无处安放，导致嵌套费脑。统一为"Where 只放表达式，布尔逻辑全由表达式构建器负责"，AND/OR 优先级由表达式树结构显式表达。

> 待深入讨论（用户提出）：表达式树如何判定优先级、自动加括号。见「待讨论事项」#1。

### 决策 3：字段引用 — 字段即描述符方法链

**结论**：字段本身就是描述符，`Users.Name.Eq("x")` 方法链。原型 `var Users = orm.Register[User]()` 全局一次。

### 决策 4：灵活 Join — 字段描述符做 ON + 投影

**结论**：
- ON 条件：`Users.DeptID.EqCol(Depts.ID)`（字段方法，类型安全，编译期校验列类型一致）
- 投影：`Users.Name.As(&view.UserName)`（字段方法）
- 结果承载：投影结构体（普通结构体或带 Col），不污染原模型
- 同名列消歧：框架分配稳定列别名 `SELECT t0.name AS c0, t1.name AS c1`
- WHERE/ON 共用 Expr 机制

**职责边界**：通用 Join 只做投影；整模型加载由关联 Preload 覆盖。

### 决策 5：关联声明 — 指针注册（0 tag）

**结论**：用类型安全函数注册关联，模型零 tag，字段名自由。

**三个方案对比**：
- tag 字符串：每关联写 tag，重命名不安全，违反"少用 tag"
- 命名约定（XxxID→外键）：零 tag 但字段名被规则绑定（隐式硬编码）
- **指针注册（选定）**：零 tag、字段名自由、重命名编译报错，代价是每关联一行注册函数

### 决策 6：关联状态 — 状态入类型

**结论**：`orm.Rel[T]`/`orm.RelMany[T]` 携带 `loaded bool` + `val`。
- `Loaded()` 显式查加载状态
- `Get()` 未加载返回 `ErrNotLoaded`
- `IsNil()` 区分"加载了但无关联"vs"未加载"

### 决策 7：默认不查关联

**结论**：不 Preload 绝不多发 SQL。与声明方式无关的独立默认行为。Preload 用 IN 批量策略避免 N+1。

### 决策 8：元数据初始化 — 显式 Register

**结论**：`orm.Register[T]("table")` 显式注册。X 方案下注册是字段描述符生效的前提，故为推荐前置。Register 同时承担表名定制、命名策略、启动 Schema 校验、预热。

### 决策 9：方言 — Dialect 接口抽象

**结论**：`Dialect` 接口（占位符/RETURNING/UPSERT/LIMIT/类型映射/标识符引用）。先 PostgreSQL，MySQL/SQLite 同接口。

---

## 三、字段描述符核心类型设计

```go
// 普通字段描述符
type Col[T any] struct {
    meta *FieldMeta   // 列名、表别名、类型信息（注册时填充）
    val  T            // 实际值（实例数据）
    set  bool         // 是否被赋值过（用于 UPDATE 局部更新）
}
// 比较/排序方法（返回 Expr/Order）：Eq/Gt/Lt/Gte/Lte/Ne/In/Like/IsNull/EqCol/Asc/Desc
// 透明接口：MarshalJSON/UnmarshalJSON、sql.Scanner/driver.Valuer、Stringer/GoStringer
// 访问：Get() T、Set(v T)、IsZero() bool

// 单值关联描述符
type Rel[T any] struct {
    meta   *RelMeta   // 关系类型、外键、引用键
    val    *T         // 加载后的值
    loaded bool       // 是否被 Preload 加载
}
// 方法：Loaded() bool、IsNil() bool、Get() (*T, error)、MustGet() *T

// 集合关联描述符
type RelMany[T any] struct {
    meta   *RelMeta
    val    []T
    loaded bool
}
// 方法：Loaded() bool、All() ([]T, error)、Len() int
```

---

## 四、完整 API 形态示例

```go
// ===== 模型：字段全包装 =====
type User struct {
    ID        orm.Col[int64]
    Name      orm.Col[string]
    Age       orm.Col[int]
    DeptID    orm.Col[int64]
    CreatedAt orm.Col[time.Time]
    Dept      orm.Rel[Dept]
    Posts     orm.RelMany[Post]
}

// ===== 注册（全局一次）=====
var Users = orm.Register[User]("users")
var Depts = orm.Register[Dept]("depts")

// ===== 关联声明（0 tag，类型安全注册，集中可见）=====
var _ = orm.BelongsTo(
    func(u *User) *orm.Rel[Dept]   { return &u.Dept },
    func(u *User) *orm.Col[int64]  { return &u.DeptID },
    func(d *Dept) *orm.Col[int64]  { return &d.ID })
var _ = orm.HasMany(
    func(u *User) *int64           { return &u.ID /* 取底层需适配，见待讨论#2 */ },
    func(p *Post) *int64           { return &p.UID },
    func(u *User) *orm.RelMany[Post] { return &u.Posts })

// ===== CRUD =====
users, _ := Users.From(db).
    Where(Users.Name.Eq("alice").And(Users.Age.Gt(18))).
    OrderBy(Users.Age.Desc()).
    Limit(10).
    All(ctx)
fmt.Println(users[0].Name.Get())

// ===== 灵活 Join =====
type UserDeptView struct {
    UserName string `db:"c0"`
    DeptName string `db:"c1"`
}
var view []UserDeptView
Users.From(db).
    Join(orm.Inner, Depts, Users.DeptID.EqCol(Depts.ID)).
    Where(Users.ID.Gt(100)).
    Select(Users.Name.As(&view.UserName), Depts.Name.As(&view.DeptName)).
    All(ctx, &view)

// ===== 关联 Preload =====
users, _ := Users.From(db).
    Preload(Users.Dept).
    Preload(Users.Posts).
    Where(Users.ID.Eq(1)).
    All(ctx)
if users[0].Dept.Loaded() && !users[0].Dept.IsNil() {
    fmt.Println(users[0].Dept.Get().Name.Get())
}

// ===== JSON 透明 =====
b, _ := json.Marshal(users[0])  // {"ID":1,"Name":"alice",...}

// ===== Raw 兜底 =====
var us []User
orm.Raw[User](&us, ctx, "SELECT * FROM users WHERE age > $1", 18)
```

---

## 五、Go 版本边界

- **Go 1.26（当前）**：字段描述符/表达式/查询/方言/扫描/关联/迁移全部可落地。字段用泛型类型的方法（`Col[T]` 上的方法是泛型类型的方法，非泛型方法，1.26 完全支持）。
- **Go 1.27（2026.8）**：增强项——投影 SELECT 多列返回精细类型（`Pick2[A,B]` 泛型方法）、UPSERT 冲突列类型化引用。**interface 不能声明泛型方法**，故这些方法挂在具体类型上。1.26 先以等价函数式 API 落地，1.27 可选升级。

---

## 六、包架构与目录结构

```
fusion/
├── go.mod                          (module fusion, go 1.26)
├── fusion.go                       顶层入口：Register[T]、Raw[T]、Tx、From（Table 上的方法）
├── col/
│   └── col.go                      Col[T]：字段描述符 + Eq/Gt/Asc/EqCol + 透明序列化接口
├── rel/
│   └── rel.go                      Rel[T]/RelMany[T]：关联描述符 + Loaded/Get/All + 状态管理
├── meta/
│   └── meta.go                     FieldMeta/RelMeta/ModelMeta + Register 反射填充 + 缓存
├── expr/
│   └── expr.go                     Expr 节点树(Leaf/And/Or/Not) + 优先级 + 自动括号 render
├── query/
│   ├── query.go                    Query[T]：From/Where/OrderBy/Limit/Join/Select/Preload/All/One/Count
│   └── dml.go                      Insert[T]/Update[T]/Delete[T]（PG RETURNING）
├── scan/
│   └── scan.go                     rows → *T 映射：Col[T] 填值、Rel 状态、列别名路由
├── builder/
│   └── builder.go                  Expr + Query → (SQL, []args)，方言感知渲染
├── dialect/
│   ├── dialect.go                  Dialect 接口
│   ├── postgres.go                 PostgreSQL（MVP 首方言）
│   ├── mysql.go                    MySQL（后续阶段）
│   └── sqlite.go                   SQLite（后续阶段）
├── relation/
│   └── relation.go                 BelongsTo/HasOne/HasMany/ManyToMany 注册 + Preload IN 策略
├── migrate/
│   └── migrate.go                  meta → DDL 生成 + schema 比对 + 启动校验
├── tx/
│   └── tx.go                       orm.Tx(ctx) 上下文事务 + savepoint 嵌套
├── hook/
│   └── hook.go                     Before/After Create/Update/Delete 回调
├── raw/
│   └── raw.go                      Raw[T]：原始 SQL + args，复用 scan 层
└── examples/                       各模块端到端示例
```

---

## 七、分阶段实施路线

- **阶段 0 — 地基（1.26）**：col + meta + expr + builder + scan + dialect/postgres + 顶层 Register/From/Raw。端到端类型安全 CRUD + WHERE + ORDER BY + LIMIT。
- **阶段 1 — DML 与事务**：dml.go（Insert/Update/Delete + PG RETURNING + Col.set 局部更新）+ tx + hook。
- **阶段 2 — 方言扩展**：mysql + sqlite，验证 Dialect 抽象。
- **阶段 3 — 关联与灵活 Join**：rel + relation（四种关联 + Preload IN + 加载状态）+ query 的 Join/EqCol/投影扫描。
- **阶段 4 — 迁移**：migrate（meta→DDL + schema 比对 + 启动校验）。
- **阶段 5 — 1.27 增强（可选）**：投影 SELECT、UPSERT 冲突列升级为泛型方法式。

---

## 八、待讨论事项（后续接着聊）

> 以下点已确认方向但尚未深入展开，留待后续讨论细化。

### 1. 表达式树优先级与自动括号 —— 已定：同类扁平 + 跨类括号 ✅

**结论**：render 采用**"同类扁平 + 跨类括号"**策略，输出接近手写 SQL 且**完全不依赖 SQL 运算符优先级**，无漏洞。

**节点类型与优先级**（数值仅用于 render 决策，不参与运算）：
| 节点 | 优先级 | 说明 |
|---|---|---|
| `Or` | 1 | 结合最松 |
| `And` | 2 | 比 Or 紧 |
| `Not` | 3 | 一元 |
| `Leaf`（比较/Isnull 等） | 4 | 叶节点 |

**render 规则（两条，无漏洞）**：
1. **同类扁平**：子节点与父节点同类（Or/Or、And/And）→ 不加括号，结合律保证等价
2. **跨类括号**：子节点与父节点不同类，且子节点是组合节点（And/Or）→ 加括号
3. **Not**：操作数为 Leaf → 不括号（`NOT D`）；操作数为组合 → 括号（`NOT (A AND B)`）

**为什么无漏洞**：
- 同类扁平依赖结合律（`(A AND B) AND C ≡ A AND (B AND C)`），数学上严格等价
- 跨类一律括号 → **不依赖 SQL 默认优先级**（AND>OR），每个 And 子树都被括号保护，无论 SQL 引擎优先级如何都不改变语义
- Not 一元运算符与组合操作数的组合，括号消除歧义

**验证示例**：
```
输入树：Or{ And{A,B}, And{C, Not{D} }, E }
输出：  (A AND B) OR (C AND NOT D) OR E
       └同类Or扁平┘ └And跨类括号┘    └Leaf┘
```

| 输入树结构 | 输出 SQL |
|---|---|
| `And{A,B,C,D}`（同类多层） | `A AND B AND C AND D`（扁平无括号） |
| `Or{And{A,B}, C}` | `(A AND B) OR C`（And 跨类括号） |
| `And{A, Or{B,C}}` | `A AND (B OR C)`（Or 跨类括号） |
| `Not{And{A,B}}` | `NOT (A AND B)`（Not+组合括号） |
| `Not{D}` | `NOT D`（Not+Leaf 无括号） |

**用户收益**：构建表达式时**永远不用背诵 AND>OR 优先级**，结构怎么组合，输出就怎么括号化，零思考、零歧义。

**render 实现草案**：
```go
func render(n node, parentKind kind) (sql string, args []any) {
    k := nodeKind(n)
    s, a := renderChildren(n, k)        // 递归子节点，传当前 kind 作为父
    if isCompound(k) && k != parentKind {
        s = "(" + s + ")"               // 跨类组合节点 → 括号
    }
    return s, a
}
```

### 2. Col[T] 取底层值的适配 —— 已定：全局注册 + 字段指针定位 ✅

**结论**：关联用**全局注册函数**，参数为**字段指针**（`*orm.Col[T]` / `*orm.RelMany[T]`），框架反射算偏移定位字段、解析列名与外键关系，元数据存入 Register 时建立的 ModelMeta，**热路径零反射**。

**决策推理**：经多轮探索（注册函数取值 / 模型实现接口 / Table 嵌入 proto / C() 访问器 / FieldRef / 字段类型自带元数据 / tag / 命名约定），确认一个核心矛盾：

> **「模型内声明 + 类型安全 + 无 tag」在 Go 1.26 + 无代码生成下，物理上不可同时满足。**
> - 模型内声明 → 只能用 tag / 命名约定 / 类型参数
> - tag、命名约定均被否决（前者字符串不安全，后者隐式硬编码）
> - 类型参数 Go 1.26 不支持字符串字面量（无法用 `FK["DeptID"]`）
> - 故唯一出路是**脱离模型、全局显式注册**

**已排除的方案**：
- 模型实现 `Relations()` 接口：跨表字段（has_many/m2m 的子表外键）在模型方法体内无法访问；且方法签名绕不开泛型方法限制（1.26 无泛型方法，1.27 不能进 interface）
- Table 嵌入 proto / C() / FieldRef：这些是"字段访问语法"层面的优化，与"声明步骤"这一核心诉求无关，属于技术自嗨，已摒弃

**注册 API 形态（草案）**：
```go
var Users = orm.Register[User]("users")
var Posts = orm.Register[Post]("posts")

// 字段指针定位（一次性，非热路径）
orm.HasMany(&Users.Posts, &Posts.UID, &Users.ID)
orm.BelongsTo(&Users.Dept, &Users.DeptID, &Depts.ID)
// 参数：集合/关联字段指针、子外键/父外键字段指针、引用键字段指针
// 框架反射算偏移 → 定位字段 → 解析列名 + 关系 → 存入 ModelMeta
```

**遗留细化点**：`Users.Posts` 这种"从 Table 取字段指针"的具体访问器形态（Table 嵌入 proto / Ref[T]() / 字段访问器）待阶段 3 实施时定，不影响整体架构。核心原则：注册是一次性配置，不在热路径，可用零值实例取址。

### 3. UPDATE 局部更新 —— 已定：set 标志 + Col[*T] 表达 NULL ✅

**结论**：
1. **局部更新靠 `Col.set bool` 标志**：只有被 `.Set()` 过的字段进 SET 子句，避免全量更新误清零值
2. **NULL 用 `Col[*T]` 表达**：nil 即 NULL，非空用 `Col[T]`，类型即语义，底层框架包办转换

#### 决策推理

**局部更新三个陷阱的解法**：

| 陷阱 | 解法 |
|---|---|
| 查出来的实例被误全量更新 | 扫描走**内部路径**（直接写 val，不置 set 标志）；只有用户 `.Set()` 才置 set |
| 零值该不该更新 | **由 set 标志决定，不由值决定**。`.Set(0)` 与 `.Set(18)` 一视同仁（全包装相对普通结构体的优势——能区分"0 是没赋值"还是"赋了 0"） |
| NULL 语义 | `Col[*T]` 表达 NULL |

**NULL 语义的关键决策**：经确认，Go 类型系统**无法阻止**用户定义 `Col[*T]` 指针类型。既然阻止不了，则**拥抱它**——让 `Col[*T]` 成为表达 NULL 的官方方式，框架原生支持。

选型为**哲学 1（NULL 是字段的类型属性，不是值的操作）**：
- `Col[T]` 非空，永远不能 NULL
- `Col[*T]` 可空，nil 即 NULL
- 两者都是 `Col[?]`，API 体验完全统一（都走 Get/Set）
- 精确匹配 SQL 的列约束语义（NULL 约束是 schema 属性）

排除的方案：
- 值类型 `Col[T]` + `SetNull()`：非空字段也带 null 标志位（冗余），Get 无法区分零值与 NULL，与 SQL 列约束语义冲突
- 专用 `Null[T]`：字段类型不统一（Col[T] vs Null[T]）

#### API 形态

```go
type User struct {
    Name     orm.Col[string]    // 非空
    Age      orm.Col[int]       // 非空
    Nickname orm.Col[*string]   // 可空，nil=NULL
}

// 局部更新（只改被 Set 的字段）
u := &users[0]
u.Name.Set("bob")
orm.Update(u).Where(Users.ID.Eq(u.ID.Get())).Exec(ctx)
// SQL: UPDATE users SET name=$1 WHERE id=$2

// 零值也更新（靠 set 标志）
u.Age.Set(0)
orm.Update(u).Exec(ctx)            // 包含 SET age=0

// NULL（Col[*T]）
u.Nickname.Set(nil)
orm.Update(u).Exec(ctx)            // SET nickname=NULL

// 扫描行为：DB NULL → 框架填 nil；DB "bob" → 填 &"bob"
if u.Nickname.Get() != nil { ... } // 用户判 nil 即知是否 NULL
```

#### 全量更新（可选）

若用户需要全量更新（如批量覆盖），提供显式 opt：
```go
orm.Update(u, orm.WithAllFields()).Exec(ctx)   // 忽略 set 标志，全字段写回
```

### 4. 多方言的 UPSERT/RETURNING 差异 —— 已定：API 统一 + Dialect 抹平 ✅

**结论**：
1. **API 层统一**：Insert 返回完整实体，Upsert 用字段指针表达冲突目标与更新列，用户感知不到三方差异
2. **Dialect 抹平底层差异**：UPSERT 语法、RETURNING 支持与否由方言渲染决定

#### 决策推理

**问题 A：MySQL 旧版无 RETURNING 的姿态** → 选**旧版退化为二次 SELECT**
- API 层统一"Insert 返回完整实体"，三方语义一致
- MySQL 旧版底层 INSERT 后按主键二次 SELECT 回填
- 用户无感，代价是 MySQL 旧版多一次查询

**问题 B：UPSERT 冲突目标与更新列的 API 表达** → 选**字段指针**
- 冲突目标、更新列都用字段引用（类型安全、零字符串）
- 更新列的值由框架从实体自动取（实体已 `.Set()`），用户不关心各方的 `VALUES()/excluded./new.` 差异

#### 三方差异矩阵

| 能力 | PostgreSQL | MySQL | SQLite |
|---|---|---|---|
| UPSERT 语法 | `ON CONFLICT (col) DO UPDATE` | `ON DUPLICATE KEY UPDATE col=VALUES(col)` | `ON CONFLICT (col) DO UPDATE` |
| 冲突目标 | 显式指定列 | 隐式（唯一键/主键自动），API 传入被忽略或仅类型检查 | 显式指定列 |
| RETURNING | ✅ 原生 | ❌ 旧版无（8.0+/MariaDB 10.5+ 有） | ✅ 3.35+ |
| 占位符 | `$1` | `?` | `?` 或 `$1` |

#### API 形态

```go
// Insert 返回完整实体（三方一致）
var u User
u.Name.Set("alice")
u.Age.Set(20)
orm.Insert(&u).Exec(ctx)   // u.ID 被回填；MySQL 旧版底层二次 SELECT

// Upsert：字段指针表达冲突目标 + 更新列
orm.Upsert(&user).
    OnConflict(Users.ID).                  // 冲突目标（字段指针）
    DoUpdate(Users.Name, Users.Age).       // 冲突时更新的列（值自动从实体取）
    Exec(ctx)
```

**三方渲染示例**：
- PG：`INSERT INTO users (...) VALUES (...) ON CONFLICT (id) DO UPDATE SET name=$1, age=$2`
- SQLite：同 PG
- MySQL：`INSERT INTO users (...) VALUES (...) ON DUPLICATE KEY UPDATE name=VALUES(name), age=VALUES(age)`（`OnConflict` 被忽略）

#### Dialect 接口形态

```go
type Dialect interface {
    Placeholder(i int) string                              // PG:$1  MySQL/SQLite:?
    QuoteIdent(name string) string                         // PG:"x"  MySQL:`x`  SQLite:"x"
    SupportsReturning() bool                               // PG/SQLite:true  MySQL旧版:false
    UpsertSQL(conflictCols, updateCols []string) string    // 渲染各方 ON CONFLICT/ON DUPLICATE KEY
    // 类型映射、自增语法等
}
```

`SupportsReturning()` 决定 Insert 实现路径：true → RETURNING 子句；false → INSERT 后二次 SELECT。

### 5. 迁移的 schema 比对策略 —— 已定：计划-执行分离，完整设计记录但不实现 ✅

**结论**：迁移采用**计划-执行分离**模式（生成计划 → 用户审核 → 决定执行）。**MVP 不实现迁移**，本节为完整设计计划 + 待决点记录，留待核心模块（查询/关联/DML）完成后再做。

**产品定位**：显式 API 调用或命令行管理（二选一或都做，见待决点 #5.3）。

#### 5.1 设计：计划-执行分离（Plan-Apply）

```go
// 第一步：算 diff，生成计划（不执行）
plan := orm.MigratePlan(db, Users, Posts /* ...所有注册的模型 */)
for _, stmt := range plan.Statements {
    fmt.Println(stmt.Kind)    // Safe / Dangerous / Risk（风险提示）
    fmt.Println(stmt.SQL)     // 将要执行的 SQL
    fmt.Println(stmt.Reason)  // 原因，如 "User 缺少列 Email"
}

// 第二步：用户决定
plan.WriteFile("001_init.sql")              // 只导出脚本，不执行
plan.Apply(ctx)                              // 执行安全项
plan.Apply(ctx, orm.WithDangerous())         // 显式 opt-in 才执行危险项
```

**操作分级**：
- **Safe**：加列、加索引、加表 → 自动进计划，Apply 默认执行
- **Dangerous**：删列、改类型、删表 → 进计划但标记，需 `WithDangerous()` 才执行
- **Risk**：加唯一索引等高风险（可能因脏数据失败）→ 自动插入风险提示步骤

#### 5.2 设计：数据清洗钩子（Before/After）

针对"加唯一索引前需去重"等 DDL 前需 DML 清洗的场景：

```go
plan := orm.MigratePlan(db, Users)

// 在特定步骤前插入自定义清洗逻辑
plan.Before("users_email_unique", func(ctx context.Context, db orm.DB) error {
    _, err := db.ExecContext(ctx, `
        DELETE FROM users WHERE id NOT IN (
            SELECT MIN(id) FROM users GROUP BY email)`)
    return err
})

plan.Apply(ctx)   // 先跑清洗，再建索引
```

**三层架构**：
1. **自动 diff 生成计划**（框架）：安全操作直接进计划，危险操作标记，高风险操作插风险提示
2. **用户介入钩子**（用户）：Before/After 插入自定义 DML（清洗），可跳过/重命名/修改步骤
3. **审核执行**（用户决定）：打印/导出脚本，显式 Apply，危险项 opt-in

#### 5.3 待决点（实现时再定）

1. **管理入口**：显式 API 调用（`orm.MigratePlan(...).Apply()`）vs 命令行工具（`fusion migrate`）vs 两者都做
   - 命令行需额外的脚本版本化机制（编号/时间戳/状态表）
2. **diff 算法的 schema 读取**：需 Dialect 暴露读取实际 schema 的能力
   - PG/MySQL：`information_schema`
   - SQLite：`sqlite_master` / `PRAGMA table_info`
3. **脚本版本化**：若支持版本化，状态记录在哪（专用 `_migrations` 表？），如何处理回滚
4. **类型映射的 diff 判定**：model 的 `Col[int64]` 对应 DB 的 `BIGINT`，类型映射表如何定义，类型不一致是否算 diff（避免 `INT` vs `INTEGER` 这种等价差异误判）
5. **回滚支持**：是否生成 down migration，还是只做单向迁移
6. **改名检测**：删列+加列 vs 改名列（改名应保留数据，diff 算法需提示词匹配）

#### 5.4 实施时机

阶段 4（关联与 Join 之后）。核心模块（查询/关联/DML/事务）优先。

### 6. 事务嵌套（savepoint）的 API 形态 —— 已定：默认模式 + 可覆盖 ✅

**结论**：`orm.Tx` 通过**全局默认事务模式**控制嵌套行为，支持单次调用覆盖。

#### 两种事务模式

| 场景 | TxModeSavepoint | TxModeReuse |
|---|---|---|
| 无外层事务 | BEGIN/COMMIT/ROLLBACK | BEGIN/COMMIT/ROLLBACK（一致） |
| 有外层事务 | SAVEPOINT/RELEASE/ROLLBACK TO（部分回滚） | 复用外层，提交/回滚是 no-op |
| 内层 error | 立即 ROLLBACK TO 当前 savepoint（撤销该单元操作，外层继续） | 不回滚（依赖外层 return err 整体回滚） |

**模式本质**："嵌套时是否支持部分回滚"的策略。savepoint 支持部分回滚，reuse 不支持但更简单。顶层事务两者行为一致。

#### 决策推理

- 用户提供"可设置默认模式，实际执行根据模式来"——即默认模式 + 可覆盖
- savepoint 透明嵌套是最优雅的（API 语义统一为"可独立提交/回滚的工作单元"），但需要用户明确选择（内层失败是否立即撤销），故做成模式
- reuse 模式是最简单常见的（sqlx/GORM 做法），适合不需要部分回滚的场景

#### API 形态

```go
// 1. 设置默认模式（初始化时）
orm.SetDefaultTxMode(orm.TxModeSavepoint)   // 推荐 savepoint 为默认

// 2. 标准调用（用默认模式）
orm.Tx(ctx, func(ctx context.Context) error {
    // ... 业务逻辑
    return nil   // nil→提交/RELEASE，error→回滚/ROLLBACK TO
})

// 3. 单次覆盖（特殊场景）
orm.TxWithMode(ctx, orm.TxModeReuse, func(ctx context.Context) error { ... })
```

#### 部分回滚示例（savepoint 模式）

```go
orm.Tx(ctx, func(ctx) error {              // BEGIN
    err := orm.Tx(ctx, func(ctx) error {   // SAVEPOINT sp1
        orm.Insert(&profile).Exec(ctx)
        return errors.New("oops")
    })                                     // ROLLBACK TO sp1（profile 撤销，事务还活着）
    if err != nil {
        log.Error(err)
        // 外层可选择继续
    }
    orm.Insert(&log).Exec(ctx)
    return nil                             // COMMIT（log 保留，profile 已回滚）
})
```

#### 实现要点

- 事务状态通过 `context.Context` 传递（`orm.Tx` 往 ctx 注入当前事务 + savepoint 计数器）
- 内层 `orm.Tx` 检测 ctx 中已有事务：无 → 开新事务；有 → 按模式决定（SAVEPOINT 或复用）
- savepoint 命名用计数器保证唯一（`sp1, sp2, ...`）

### 7. ManyToMany 的连接表元数据如何提供 —— 已定：连接表强制建模（B）✅

**结论**：m2m 的连接表**强制建模为独立模型**（`orm.Register[JoinModel]`），即使纯 m2m（连接表无额外字段）也建一个只含两个外键的模型。保持 #2 的"字段指针、零字符串"一致性。

**决策推理**：
- m2m 路径是两段：`User ——(左外键)—— JoinTable ——(右外键)—— Post`，比 belongs_to/has_many 多"连接表 + 第二个外键"
- 若连接表用纯表名（思路 A），外键列名必然是字符串（`"user_id"`），破坏 #2 的类型安全原则
- 强制建模（思路 B）是 A 的超集：纯 m2m 时连接表模型只有两个外键字段，带属性时加额外字段；且连接表可独立查询/操作

**注册 API 形态（与 belongs_to/has_many 同构）**：
```go
type UserPost struct {
    UserID orm.Col[int64]
    PostID orm.Col[int64]
    // 纯 m2m 到此为止；带属性时加 Role/CreateAt 等
}
var UserPosts = orm.Register[UserPost]("user_posts")

orm.ManyToMany(
    &Users.Posts,                              // 集合字段（回填位置）
    UserPosts,                                 // 连接表（Table 对象）
    &UserPosts.UserID, &Users.ID,              // 左路径：连接表左外键 → 父主键
    &UserPosts.PostID,  &Posts.ID,             // 右路径：连接表右外键 → 子主键
)
```

**四种关联形态统一**：
| 关联 | 签名模式 |
|---|---|
| BelongsTo | `(关联字段, 外键, 引用键)` |
| HasOne | `(关联字段, 外键, 引用键)` |
| HasMany | `(集合字段, 外键, 引用键)` |
| ManyToMany | `(集合字段, 连接表, 左外键+引用键, 右外键+引用键)` |

前三个是三元组，m2m 是"三元组 × 2 + 连接表"，完全零字符串。

**Preload SQL 形态（IN 批量，避免 N+1）**：
```sql
① SELECT * FROM users WHERE id = 1              -- 收集父 ID
② SELECT * FROM user_posts WHERE user_id IN (1) -- 经连接表收子 ID
③ SELECT * FROM posts WHERE id IN (10,20)       -- 一次查回子记录
④ 按 user_id → post_id 组装进对应 user.Posts
```

**额外红利**：连接表建模后，步骤②可用连接表模型做复杂查询（如带 Role 过滤的 m2m），纯表名方案做不到。

### 8. 1.27 泛型方法的具体增强点 —— 已定：远期可选项，当前不投入 ✅

**结论**：1.27 泛型方法定位为**远期可选项**，MVP 不投入。文档记录潜在增强点，待 1.27 稳定、社区有最佳实践后再评估。

#### 决策推理

经审视三个潜在增强点，结论是**增强价值有限**：

| 增强点 | 价值 | 理由 |
|---|---|---|
| 投影 SELECT 类型化多列返回（`Select2[A,B]`→`Tuple[A,B]`） | 中 | 自定义结构体（带 `db` 标签）已覆盖投影需求，Tuple 边际收益小；且 `Select2/Select3/...SelectN` 需为每个 N 写方法，繁琐 |
| UPSERT 冲突列类型化引用 | 低 | 1.26 下 `DoUpdate(Users.Name, Users.Age)` 已类型安全（字段描述符自带类型），1.27 仅略微增强约束 |
| Preload 关联过滤的类型安全（`Preload2(field, func(*Child) Expr)`） | 中高 | 1.26 下 Preload 带过滤条件较难做类型安全，1.27 能改善，但非核心能力 |

#### 不投入的核心理由

1. **1.26 已覆盖全部核心能力**：字段描述符/表达式/查询/方言/关联/迁移（设计）/事务，均不依赖泛型方法
2. **interface 限制**：1.27 下 **interface 仍不能声明泛型方法**，若 `Query[T]` 进 interface（如 mock/测试），泛型方法会成为障碍，**反而降低可测试性**
3. **时机未到**：1.27（2026.8）尚未稳定，社区最佳实践未形成

#### 潜在增强点记录（未来评估用）

- 投影 SELECT：`Select2[A,B]`/`Select3[A,B,C]` 返回 `Query2[T,A,B]`，All 返回 `[]Tuple[A,B]`
- UPSERT：冲突列携带"冲突时更新"语义的更精细类型约束
- Preload：`PreloadFilter(field, func(*Child) Expr)` 类型安全的关联条件过滤

#### 升级时机判断标准

当以下条件满足时再评估升级：
- Go 1.27 正式发布且社区有泛型方法的最佳实践
- 出现 1.26 API 无法优雅覆盖的真实需求
- 确认不破坏可测试性（interface 设计不依赖泛型方法）
