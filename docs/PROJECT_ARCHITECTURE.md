# 业务项目架构设计（多商家多门店 SaaS）

> **本文档是业务项目的架构设计，不属于 fusion ORM 库本身。** 它是 fusion 作者基于 fusion
> 开发实际业务项目（多商家多门店商城 + 收银 + 进销存 + 财务）时的架构决策记录。
> 新对话/新项目应把此文件作为 `ARCHITECTURE.md` 放在项目根目录，作为 AI 生成的"宪法"。
>
> 产出时间：2026-07。ORM 库：github.com/sth4me/fusion。目标 DB：PostgreSQL（17.x，未来 18/19）。

---

## 一、业务全貌

### 1.1 业务模块

| 模块 | 职责 | 负载特征 | 一致性要求 |
|---|---|---|---|
| **商城**（storefront） | 前端卖货：商品/购物车/下单/营销 | 读多写少、高并发浏览 | 最终一致 |
| **收银**（pos） | 线下 POS 终端接入、当面付 | 低延迟、不能错账 | **强一致**（扣库存+生成订单+记账，事务链） |
| **进销存**（inventory） | 采购、库存、盘点 | 批量写入、库存扣减 | **强一致** |
| **财务**（reporting/finance） | 报表、对账、财务分析 | 读、异步 | 最终一致 |
| **平台**（platform） | 租户管理、用户、权限、认证 | 中频 | 强一致 |

### 1.2 模块耦合关系（决定微服务怎么拆）

```
商城(前端卖货) ──┐
                ├──→ 订单 ──→ 库存扣减 ──→ 记账
收银(线下POS) ──┘         (强一致事务链)      │
                                           财务报表 ←──┘
```

**核心洞察：订单、库存、账务是共享的强一致核心域；商城和收银只是两个接入入口。**
这个认知决定：不能按"商城/收银/进销存/财务"表面切微服务，否则订单→库存→账务事务链被切断。

### 1.3 组织模型（多商家多门店 + 加盟嵌套）

```
平台(我们)
 └─ 商家(merchant)
     ├─ 直营商家 ─── 门店1, 门店2, 门店3 ...
     └─ 加盟商家
         ├─ 一级加盟 ─── 门店 ...
         │   └─ 二级加盟(嵌套) ─── 门店 ...
         └─ ...
```

- **起步场景**：主要满足直营商家 1-3 个门店的需求。
- **加盟有级别**（嵌套树形），不是扁平的"平台-商家-门店"三层。
- 加盟的树形结构影响：tenant 粒度、权限的"子树范围"授权。

---

## 二、架构决策总览

| 维度 | 决策 | 理由摘要 |
|---|---|---|
| **微服务化** | 模块化单体起步，核心域永不拆、适配层未来可拆 | 避免过早分布式事务地狱；订单/库存/账务强一致链保持本地事务 |
| **框架** | go-zero（推荐）或 Gin（MVP 起步）+ fusion | go-zero 微服务能力匹配规模；goctl 生成+AI 填 logic 最顺；fusion 不冲突 |
| **领域分层** | domain（纯 Go）+ repo（fusion 实现）+ model + api（框架壳） | 业务规则框架无关；fusion 调用只在 repo；AI 生成稳 |
| **主键 ID** | **UUIDv7**，应用层生成 | 不可枚举（多租户安全）+ 未来微服务零迁移 + 离线可生成 + 索引友好 |
| **多租户** | 共享库 + merchant_id + store_id + PG RLS | 门店为隔离最小单元；RLS 兜底防漏；不用 schema-per-tenant |
| **权限** | 自写 4 表 RBAC + scope（store/merchant/subtree/platform） | 数据权限 casbin 帮不上；自写标准模式 AI 最稳 |
| **加盟树** | 闭包表（closure table） | 查子树 O(1)；写少读多完美匹配 |
| **认证** | JWT（无状态，适配多端 H5/小程序/APP） | |
| **平台运营** | 算特殊 scope=platform，可看全平台数据 | |

---

## 三、微服务化策略：模块化单体 + 领域边界

### 3.1 为什么不现在拆微服务

- 初期主要靠 AI 实现生产力，分布式事务/服务发现/链路追踪基础设施成本吃掉大部分精力
- 订单→库存→账务是强一致链，拆服务后要用 Saga/TCC/Outbox——工程地狱，无流量压力时无收益
- 团队规模（人 + AI）远没到需要物理隔离协作

### 3.2 为什么不无脑单体

单体内部必须按领域边界严格隔离，让未来拆分摩擦最小。

### 3.3 目录结构（模块化单体）

```
yourapp/
├── core/                          ← 核心域（强一致，永不拆成多个服务）
│   ├── order/                     ← 订单聚合（商城和收银共用）
│   ├── inventory/                 ← 库存聚合
│   └── billing/                   ← 账务聚合
├── storefront/                    ← 商城前端适配（商品/购物车/营销，读多）
├── pos/                           ← 收银前端适配（POS 终端接入，低延迟）
├── reporting/                     ← 报表/财务分析（读，异步）
└── platform/                      ← 平台层（租户管理、用户、权限、认证）
```

**每个域内部**：独立的 domain（聚合根+规则）、独立的 repo（fusion 实现）、独立的 model。
**域之间**通过接口或领域事件通信，不直接 import 对方内部。

### 3.4 关键规则

- `storefront` 不能直接调 `core/inventory` 内部，通过 core 暴露的应用服务接口
  （如 `OrderService.Submit()`，内部编排订单+库存+账务）
- 未来拆，拆的是 `storefront`/`pos`/`reporting`（HTTP 入口，天然适合独立服务）
- **core（order/inventory/billing）几乎永远不互相拆**——强一致事务是命脉

### 3.5 演进触发线

| 信号 | 动作 |
|---|---|
| 商城大促流量拖垮收银 | storefront 拆出独立服务 |
| 报表查询拖慢核心交易 | reporting 拆出 + 读写分离 |
| 团队 >5 人，多人改同一个 core | 谨慎考虑 core 内部按聚合拆 |
| 收银需离线/边缘部署（门店断网） | pos 拆成可离线独立服务 |

---

## 四、框架选型：go-zero（或 Gin 起步）+ fusion

### 4.1 推荐 go-zero

- 微服务优先（zRPC/gRPC、服务发现、限流熔断、链路追踪内置）
- goctl 生成 handler/logic/types/svc 骨架，AI 填 logic 业务最顺
- fusion 在 logic/service 层调 DB，与 go-zero 解耦不冲突
- 渐进路径：先 Gin 单体跑通 MVP（业务验证），再按领域拆 go-zero 微服务

### 4.2 fusion 与 go-zero 的结合点

go-zero 的分层把"用什么数据库"留给 logic 层：
- `svc.ServiceContext` 放一个 `*fusion.Engine`（或 per-tenant Engine）
- logic 层写 `fusion.EFrom(engine, model.Users)`，go-zero 不关心
- go-zero 自带的 sqlx model 层**不用**，用 fusion 替代

### 4.3 Go 1.26 限制（关键）

Go 1.26 **不支持泛型方法**（只有类型能有类型参数）。因此 Engine 的查询入口是
**E 前缀顶层函数**（不是方法）：`fusion.EFrom(engine, t)` / `EInsert` / `EUpdate` /
`EDelete` / `EDeleteByID` / `EDeleteByIDs` / `EInsertBatch` / `EUpsert` / `ERaw` / `ETx`。
等 Go 1.27 release（2026.08 预期）支持泛型方法后，可平滑改为 `engine.From(t)`。

---

## 五、领域分层规范（AI 生成的核心约束）

### 5.1 分层与职责（消除"业务写哪层"的歧义）

```
yourapp/
├── domain/              ← 领域层（纯 Go，框架无关，AI 主战场）
│   ├── order/
│   │   ├── order.go     ← 聚合根 + 业务规则（状态机、价格计算）
│   │   └── repo.go      ← 仓储接口（interface，不含实现）
│   └── inventory/
│       └── ...
├── repo/                ← 仓储实现（用 fusion 实现 domain 的接口）
│   └── order_repo.go    ← OrderRepo 接口的 fusion 实现
├── model/               ← fusion 模型 + RegisterAll
│
└── api/                 ← go-zero 工程（壳）
    ├── handler/         ← goctl 生成，不动
    ├── logic/           ← 极薄：解析参数 → 调 domain → 返回
    ├── types/           ← goctl 生成
    └── svc/             ← 注入 fusion.Engine + repo 实现
```

### 5.2 每层职责硬规则

| 层 | 写什么 | 不写什么 |
|---|---|---|
| `domain/order/order.go` | 聚合根方法、业务规则（`Order.Submit()`、状态机、价格计算） | **不 import fusion、不 import go-zero、不查库** |
| `domain/order/repo.go` | 仓储**接口**（`type OrderRepository interface{ Save(ctx, *Order) }`） | 不写实现 |
| `repo/order_repo.go` | 用 fusion 实现 `OrderRepository`：`fusion.From(model.Orders, db).Where(...)` | 不写业务规则，只做接口↔fusion 翻译 |
| `model/` | fusion 模型类型 + RegisterAll | 不写逻辑 |
| `api/logic/` | 极薄编排：`order := domain.NewOrder(req)` → `repo.Save(ctx, order)` → `return resp` | **不写业务规则、不直接调 fusion** |
| `api/svc/` | 注入依赖（`OrderRepo: repo.NewOrderRepo(engine)`） | |

**核心规则（写进 AI 的 system prompt）**：
- 业务规则只在 `domain/` 里
- fusion 调用只在 `repo/` 里
- `logic/` 只做参数翻译 + 调 domain + 调 repo，不碰业务细节、不碰 fusion

### 5.3 层数对比（为什么不"层过多"）

| 方案 | 层数 | 问题 |
|---|---|---|
| go-zero 原生 | handler/logic/model | 业务复杂时 logic 膨胀成面条 |
| 嵌套完整 DDD | handler/logic/service/domain/repository/model | 6 层，service 和 logic 抢职责 |
| **本方案** | handler/logic/domain+repo/model | 4 层，每层职责唯一不重叠 |

砍掉独立 service 层——go-zero 的 logic 已是应用服务层，没必要再叠。

### 5.4 logic 层示例（4 步翻译模板）

```go
func (l *SubmitLogic) Submit(req *types.SubmitReq) (*types.SubmitResp, error) {
    // 1. 翻译请求为领域对象
    order := domain.NewOrder(req.UserID, req.Items, l.svcCtx.IDGen)
    // 2. 调领域方法（业务规则在 domain）
    if err := order.Submit(req.Items); err != nil {
        return nil, err
    }
    // 3. 事务边界 + 持久化（fusion.Tx 包多步操作）
    err := fusion.Tx(l.ctx, l.svcCtx.DB, func(ctx context.Context) error {
        return l.svcCtx.OrderRepo.Save(ctx, order)
        // 收银场景这里还能加：扣库存、记流水，全在一个事务
    })
    // 4. 翻译响应
    return &types.SubmitResp{OrderID: order.ID()}, err
}
```

---

## 六、主键 ID：UUIDv7（应用层生成）

### 6.1 为什么不用自增 ID

| 问题 | 说明 |
|---|---|
| 多租户可枚举 | 连续 ID 易被试探其他租户订单（改 URL ?id=1001→1000） |
| 微服务化迁移噩梦 | 自增 → UUID 要改主键+所有外键+所有关联表，跨数据层改造 |
| INSERT 热点页 | 自增顺序写入，B-tree 最右页锁竞争 |
| 离线生成不可行 | POS 断网收银无法分配 ID |

### 6.2 为什么是 UUIDv7（不是 v4）

UUIDv7 = `48位Unix毫秒时间戳 + 4位版本 + 12位随机`：
- **单调递增**（索引友好，B-tree 写入局部集中，不像 v4 全表打散）
- **可按 ID 排序近似按时间排序**（省去 created_at 索引在某些场景）
- **多节点同时生成不冲突**

### 6.3 ID 类型设计（领域层）

```go
// domain/id.go
package domain

import "github.com/google/uuid"

// ID 是领域标识，基于 UUIDv7。
type ID uuid.UUID

func (id ID) IsZero() bool { return id == ID{} }
func (id ID) String() string { return uuid.UUID(id).String() }

// IDGenerator 领域层定义接口，infra 实现。
type IDGenerator interface {
    NextID() ID
}

// UUIDv7Generator infra 实现。
type UUIDv7Generator struct{}
func (UUIDv7Generator) NextID() ID {
    u, _ := uuid.NewV7()
    return ID(u)
}
```

### 6.4 领域聚合的 ID 处理

```go
// domain/order/order.go
type ID domain.ID  // 独立类型，编译期防混淆（OrderID ≠ ProductID）

type Order struct {
    id ID  // 小写不导出，强制通过方法访问
    // ...
}

func (o *Order) ID() ID { return o.id }

// AssignID 由仓储持久化后回填（若用 DB 自增才需要；UUIDv7 创建即有 ID，不用回填）
func (o *Order) AssignID(id ID) {
    if !o.id.IsZero() { panic("order already has id") }
    o.id = id
}

func NewOrder(merchantID, storeID ID, gen IDGenerator) *Order {
    return &Order{
        id: gen.NextID(),  // 创建即有 UUIDv7，不等 DB
        // ...
    }
}
```

### 6.5 仓储层 ID 翻译

```go
// repo/order_repo.go
func (r *OrderRepo) Save(ctx context.Context, o *order.Order) error {
    po := toPO(o)
    // UUIDv7 应用层预生成，Insert 时直接带（不靠 DB RETURNING 回填）
    return fusion.EInsert(r.engine, model.Orders, po).Exec(ctx)
}

func toPO(o *order.Order) *model.OrderPO {
    po := &model.OrderPO{}
    po.ID.Set(uuid.UUID(o.ID()))  // domain.ID(uuid.UUID) → col.Col[uuid.UUID]
    // ...
    return po
}
```

### 6.6 数据库列

```sql
id uuid PRIMARY KEY,                -- 不是 BIGINT SERIAL
merchant_id uuid NOT NULL,
store_id uuid NOT NULL,
order_no TEXT UNIQUE                -- 业务单号（人可读，如 ORD-20260701-0001），与主键 ID 分开
```

### 6.7 fusion 已验证支持 UUID

`col.Col[uuid.UUID]` 和 `col.Col[*uuid.UUID]`（可空）在 SQLite + PG 原生 uuid 类型上
往返验证通过（含 NULL、按 UUID 查询、应用层预生成）。fusion 原生支持，无需补强。
机制：google/uuid 实现了 driver.Valuer + sql.Scanner，fusion 的 assignReflect 优先委托。

### 6.8 版本时间线（不用等）

- **Go 生成 UUIDv7**：现在用 `github.com/google/uuid`（NewV7），不依赖 1.27 标准库
- **PG 存 UUID**：PG 8+ 就有 uuid 类型，不依赖 18
- PG 18 把 UUIDv7 生成做成原生内置（不依赖扩展）——锦上添花，非前提
- 等 Go 1.27/PG 18/19 release 时，生成函数可换标准库/DB 端，是一行代码改动

---

## 七、多租户：共享库 + merchant_id + store_id + PG RLS

### 7.1 tenant 粒度：门店为隔离最小单元

- 收银/进销存强一致场景发生在门店内（门店 A 不能影响门店 B）
- 门店间数据天然隔离，是 RLS 天然边界
- 商家是门店归属（聚合视图），平台是全局视图

### 7.2 表结构（双租户列，非单一 tenant_id）

所有业务表加两列：

```sql
merchant_id uuid NOT NULL,   -- 商家（聚合层：商家老板看自己所有门店的总览）
store_id    uuid NOT NULL    -- 门店（隔离层：门店间互不可见）
```

- `store_id`：**隔离边界**。RLS 严格按 store_id 过滤。
- `merchant_id`：**聚合维度**。商家老板总览走 merchant_id 聚合（显式跨门店读，走商家级权限放行，不受门店 RLS 限制）。

### 7.3 RLS 策略（PG 行级安全，数据库层兜底）

```sql
-- 门店级强隔离（默认）
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY store_isolation ON orders
  USING (store_id = current_setting('app.store_id', true)::uuid);

-- 应用层中间件设置 PG session 变量
-- db.ExecContext(ctx, "SET LOCAL app.store_id = $1", storeID)
-- 之后该连接所有查询自动被 RLS 过滤
```

应用层中间件解析 token 后，设置 PG 会话变量：

```go
// 中间件：从 JWT 解析当前操作的 merchant_id + store_id，注入 PG session
db.ExecContext(ctx, "SET LOCAL app.store_id = $1", storeID)
// 之后该连接所有查询自动被 RLS 过滤到当前门店
// 即使 AI 写漏 WHERE store_id，数据库层兜底不泄露
```

### 7.4 双层语义

- `store_id`：**隔离边界**。门店 A 绝不能看门店 B 的订单/库存。RLS 策略严格按 store_id 过滤。
- `merchant_id`：**聚合维度**。商家老板要看自己所有门店的总览，按 merchant_id 聚合查询（这是有意的跨门店读，走商家级权限放行，不受门店 RLS 限制）。

### 7.5 三重保险

1. 功能权限（中间件挡 order:create）
2. 仓储层 scope 过滤（fusion Where(store_id=?)）
3. PG RLS（数据库兜底，防漏）

### 7.6 为什么不用 schema-per-tenant

起步场景（直营 1-3 门店）数据量小，schema-per-tenant DDL 扇出运维重、收益零。
共享库 + RLS 撑到几百门店无压力。大租户/合规需求出现时再迁。

---

## 八、加盟树：闭包表

### 8.1 表结构

```sql
CREATE TABLE merchants (
    id uuid PRIMARY KEY,
    name TEXT,
    type TEXT,        -- 'direct' | 'franchise'
    level INT,        -- 0=直营/平台自营，1=一级加盟，2=二级...
    parent_id uuid    -- 直接父级（邻接，用于直接关系）
);

-- 闭包表：存所有祖先-后代对（含自身），查子树 O(1)
CREATE TABLE merchant_closure (
    ancestor_id   uuid,
    descendant_id uuid,
    depth         INT,   -- 0=自身，1=直接子，2=孙...
    PRIMARY KEY (ancestor_id, descendant_id)
);
```

### 8.2 查子树

```sql
-- 查加盟商 X 管理的所有下级商家（含自身）
SELECT descendant_id FROM merchant_closure WHERE ancestor_id = X;
```

闭包表写成本稍高（插入节点写 N 行），但商家树变更低频，查询高频，完美匹配读写比。

---

## 九、权限：4 表 RBAC + scope

### 9.1 为什么不用 casbin

- 多租户策略配置复杂（RBAC with domains），AI 配 model.conf 易隐性出错
- 数据权限（"只能操作本门店订单"）casbin 帮不上，必自写
- 既然数据权限必写，功能权限自写也简单，不引入复杂度

### 9.2 功能权限（4 张表）

```
users          ← 平台用户（收银员、店长、商家老板、平台管理员）
roles          ← 角色（per merchant，如"店长""收银员""财务""加盟商"）
permissions    ← 权限码（order:create, inventory:adjust, finance:view_merchant）
role_perms     ← 角色-权限关联
user_roles     ← 用户-角色关联（带 scope，见下）
```

权限码用 `模块:操作` 格式（order:create），不用 casbin 四元组——AI 理解零歧义。

### 9.3 数据范围（user_roles 加 scope）

```sql
CREATE TABLE user_roles (
    user_id    uuid,
    role_id    uuid,
    scope_type TEXT,      -- 'store' | 'merchant' | 'subtree' | 'platform'
    scope_id   uuid,      -- store_id / merchant_id / 子树根 merchant_id / 0=全局
    PRIMARY KEY (user_id, role_id, scope_type, scope_id)
);
```

### 9.4 场景对照

| 用户 | 角色 | scope_type | scope_id | 能做什么 |
|---|---|---|---|---|
| 收银员张三 | cashier | store | 门店5 | 只在门店5收银 |
| 店长李四 | manager | store | 门店5 | 管门店5订单/库存/报表 |
| 直营商家老板 | merchant_owner | merchant | 商家2 | 看商家2所有门店总览 |
| 一级加盟商 | franchise_l1 | subtree | 商家10 | 管商家10及下挂二级+门店 |
| 平台运营 | platform_admin | platform | 0 | 全平台 |

### 9.5 权限检查统一逻辑

```go
// 仓储层根据 scope 自动追加过滤（业务层无感）
func (r *OrderRepo) List(ctx context.Context) {
    ac := auth.FromCtx(ctx)
    q := fusion.EFrom(r.engine, model.Orders)
    switch ac.EffectiveScope() {
    case ScopeStore:
        q = q.Where(model.Orders.Proto.StoreID.Eq(ac.StoreID))
    case ScopeMerchant:
        q = q.Where(model.Orders.Proto.MerchantID.Eq(ac.MerchantID))
    case ScopeSubtree:
        subtreeMerchantIDs := loadSubtree(ac.MerchantID)  // 闭包表查子树
        q = q.Where(model.Orders.Proto.MerchantID.In(subtreeMerchantIDs))
    case ScopePlatform:
        // 无过滤
    }
}
```

精髓：业务代码（domain 层）完全不感知权限范围，仓储层根据 context 自动加过滤。

### 9.6 权限缓存

用户权限码加载一次后缓存（Redis 或进程内 map + TTL），变更时失效。

---

## 十、认证：JWT

- 无状态，适配多端（H5/小程序/APP/POS）
- JWT payload 含 user_id + 当前操作的 scope（store/merchant/subtree/platform）
- 中间件解析 JWT → 加载 user_roles（含 scope）→ 注入 context
- 平台运营人员算 scope=platform 的特殊用户

---

## 十一、架构总图

```
                    ┌─────────────────────────────────────────┐
                    │            API Gateway / BFF             │
                    │  (认证中间件 → 租户中间件 → 权限中间件)   │
                    └────────┬──────────────┬─────────────────┘
                             │              │
              ┌──────────────▼──┐  ┌────────▼────────┐
              │   storefront    │  │      pos        │   ← 前端适配层
              │  (商城：读多)    │  │  (收银：低延迟)  │   （未来可拆独立服务）
              └──────┬──────────┘  └────────┬────────┘
                     │     都调用 core 的应用服务     │
              ┌──────▼────────────────────────▼──────┐
              │              core (核心域)              │
              │  ┌─────────┬───────────┬───────────┐  │
              │  │ order   │ inventory │  billing  │  │   ← 强一致核心
              │  │ (订单)   │ (库存)    │  (账务)   │  │   （永不拆分，本地事务）
              │  └─────────┴───────────┴───────────┘  │
              │         本地事务保证一致性              │
              └──────┬───────────────────────────────┘
                     │
              ┌──────▼──────┐
              │  reporting  │   ← 读/异步，可读写分离拆出
              │  (报表/财务) │
              └─────────────┘

              ┌─────────────────────────────────────┐
              │  platform (平台层)                    │
              │  租户管理 / 用户 / RBAC权限 / 认证     │
              └─────────────────────────────────────┘

              数据层：共享库 + merchant_id + store_id + PG RLS
              fusion 仓储实现 + Engine 管理
              主键 UUIDv7（应用层生成）
```

---

## 十二、演进路线

| 阶段 | 形态 | 触发条件 |
|---|---|---|
| **阶段 1（起步）** | 模块化单体，核心域+适配层同进程，共享库+RLS | 现在 |
| **阶段 2（流量分化）** | storefront 拆出独立服务 | 商城流量拖垮 POS |
| **阶段 3（读写分离）** | reporting 拆出 + 读副本 | 报表拖慢交易 |
| **阶段 4（合规/规模）** | 大租户迁独立 schema | 合规需求或租户规模 |

**核心域（order/inventory/billing）从阶段 1 到永远，保持单体内部强一致。**

---

## 十三、给 AI 的生成规范（system prompt 要点）

### 13.1 分层规范

```
1. 业务规则只在 domain/ 里；domain 不 import fusion/go-zero/不查库
2. fusion 调用只在 repo/ 里；repo 不写业务规则，只做接口↔fusion 翻译
3. logic/ 只做参数翻译 + 调 domain + 调 repo，不碰业务细节、不碰 fusion
4. 每层职责唯一不重叠
```

### 13.2 ID 规范

```
1. 领域层 ID：每个聚合定义独立 ID 类型（type OrderID domain.ID），带 IsZero()
2. ID 用 UUIDv7，应用层生成（gen.NextID()），不等 DB
3. ID 零值 = 未持久化新对象；非零 = 已持久化
4. ID 翻译只在仓储 toPO/fromPO 里做 domain.ID ↔ col.Col[uuid.UUID]
5. 领域事件/方法签名用 domain ID 类型，不暴露 uuid.UUID
6. 跨聚合引用用对方的 ID 类型（Order.CustomerID 是 CustomerID 类型）
```

### 13.3 多租户规范

```
1. 所有业务表带 merchant_id + store_id（uuid）
2. 门店是隔离最小单元（RLS 按 store_id）
3. 仓储层根据 context 的 scope 自动追加过滤（业务层无感）
4. PG RLS 兜底（中间件 SET LOCAL app.store_id）
5. 不在业务代码里手动加 tenant_id 过滤（防漏，靠仓储统一处理）
```

### 13.4 权限规范

```
1. 权限码用 模块:操作 格式（order:create）
2. 数据范围用 scope_type（store/merchant/subtree/platform）
3. 权限检查在中间件（功能）+ 仓储层（数据），domain 层不感知
4. 加盟子树用闭包表查 descendant_id IN (...)
```

### 13.5 事务规范

```
1. 强一致场景（收银/库存/账务）用 fusion.Tx 包事务边界
2. fusion.Tx 支持隔离级别 + 死锁重试（WithRetry/WithIsolation）
3. 事务边界在 service/logic 层（编排多步），不在 domain 层
```

---

## 十四、fusion ORM 关键 API 速查

### 14.1 模型定义与注册

```go
// model/order.go
type OrderPO struct {
    ID         col.Col[uuid.UUID]
    MerchantID col.Col[uuid.UUID]
    StoreID    col.Col[uuid.UUID]
    Status     col.Col[string]
    Total      col.Col[int64]
    // ...
}

// model/registry.go
var Orders *meta.Table[OrderPO]
func RegisterAll() {
    Orders = fusion.Register[OrderPO]("orders")
}
```

### 14.2 查询（全局 API，单库场景）

```go
fusion.From(model.Orders, db).Where(model.Orders.Proto.Status.Eq("paid")).All(ctx)
fusion.From(model.Orders, db).Where(model.Orders.Proto.ID.Eq(id)).One(ctx)
```

### 14.3 查询（Engine API，多库场景）

```go
engine := fusion.New(db, fusion.WithDialect(dialect.PostgresDialect))
fusion.EFrom(engine, model.Orders).Where(...).All(ctx)
```

### 14.4 DML

```go
fusion.Insert(model.Orders, db, &po).Exec(ctx)           // 单条
fusion.InsertBatch(model.Orders, db, []*OrderPO{...}).Exec(ctx) // 批量
fusion.Update(model.Orders, db, &po).Exec(ctx)            // 按 set 标志局部更新
fusion.DeleteByID(model.Orders, db, id).Exec(ctx)         // 单列 PK
fusion.DeleteByIDs(model.Orders, db, map[string]any{...}).Exec(ctx) // 复合 PK
```

### 14.5 事务（隔离级别 + 死锁重试）

```go
fusion.TxWith(ctx, db,
    func(ctx context.Context) error {
        // 收银：扣库存 + 生成订单 + 记账，全在一个事务
        return errors.Join(
            inventoryRepo.Deduct(ctx, order.Items),
            orderRepo.Save(ctx, order),
            billingRepo.Record(ctx, order),
        )
    },
    fusion.WithIsolation(sql.LevelSerializable),
    fusion.WithRetry(3, 5*time.Millisecond, 100*time.Millisecond),
)
```

### 14.6 关联预加载

```go
fusion.From(model.Users, db).
    Preload("Posts").              // 单层
    Preload("Posts.Comments").     // 嵌套
    Where(...).All(ctx)
```

### 14.7 反向迁移（启动期校验漂移 + 外键自动关联）

```go
cat, _ := fusion.LoadSchema(ctx, db, dialect.PostgresDialect, "orders", "users")
fusion.MustBind(cat, model.Orders)          // 漂移则 panic（启动 fail-fast）
fusion.AutoRegisterRelations(cat)           // 外键→自动 BelongsTo/HasMany（手动优先）
```

### 14.8 Col 操作

```go
po.Status.Set("paid")     // 写 + 标记 dirty
po.Status.Get()           // 读
po.Status.Reset()         // 清除 dirty（配合 Update 的 AllFields）
po.ID.IsSet()             // 是否被 Set 过
```

### 14.9 敏感字段脱敏

```go
fusion.AddSensitiveColumn("password", "api_key")  // 日志中这些列的值替换为 ***
```

---

## 十五、技术栈版本基线

| 组件 | 版本 | 备注 |
|---|---|---|
| Go | 1.26（当前），1.27（2026.08 预期） | 1.27 支持泛型方法，Engine API 可从 E 前缀函数改为方法 |
| PostgreSQL | 17.x（当前），18/19（未来） | 生产目标。17 完全够用；18 原生 UUIDv7 生成（锦上添花） |
| fusion | github.com/sth4me/fusion | 自研 ORM，已 PG/MySQL/SQLite 三库验证 |
| go-zero | 最新稳定版 | 微服务框架（推荐） |
| google/uuid | 最新 | UUIDv7 生成 |
| pgx/v5 | 最新 | PG 驱动 |

---

## 十六、起步阶段（直营 1-3 门店）的实施顺序

1. **platform 域**：商家/门店/用户/RBAC 表结构 + fusion 模型 + 闭包表 + RLS 配置 + 认证+租户+权限三个中间件
2. **一个 core 域示例**（如 order）：完整 domain + repo + model，作为后续各域的模板
3. **api 层骨架**：go-zero（或 Gin）的入口，含中间件链
4. **跑通一个端到端流程**：创建商家→门店→用户→下单→查单，验证全链路
5. **按域扩展**：商城/收银/进销存/财务各域照 order 模板扩

骨架跑通后，AI 按模板扩各业务域，结构一致性有保障。
