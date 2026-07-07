// Package fusion 是泛型 ORM 的顶层入口。
//
// 提供模型注册（Register）、查询入口（From）、原始 SQL 兜底（Raw）。
// 详见 docs/DESIGN.md。
package fusion

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sth4me/fusion/col"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/hook"
	"github.com/sth4me/fusion/logging"
	"github.com/sth4me/fusion/meta"
	"github.com/sth4me/fusion/schema"
	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/relation"
	"github.com/sth4me/fusion/query"
	"github.com/sth4me/fusion/scan"
	"github.com/sth4me/fusion/tx"
)

// 默认方言（全局）。可由 SetDefaultDialect 修改；读写在 defaultDialectMu 下，
// 消除原无锁访问的 data race。
var (
	defaultDialectMu sync.RWMutex
	defaultDialect   dialect.Dialect = dialect.PostgresDialect
)

// SetDefaultDialect 设置全局默认方言（影响 From/Insert/... 等全局入口）。
func SetDefaultDialect(d dialect.Dialect) {
	defaultDialectMu.Lock()
	defaultDialect = d
	defaultDialectMu.Unlock()
}

// DefaultDialect 返回当前全局默认方言。
func DefaultDialect() dialect.Dialect {
	defaultDialectMu.RLock()
	defer defaultDialectMu.RUnlock()
	return defaultDialect
}

// DB 是执行查询所需的最小接口（*sql.DB / *sql.Tx 满足）。
type DB = query.QueryExecer

// Open 打开数据库连接，返回原始 *sql.DB（供 Close/Ping）和 ctx 感知的 DB。
// d 作为全局默认方言（调 SetDefaultDialect）。
func Open(driverName, dsn string, d dialect.Dialect) (*sql.DB, DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, nil, err
	}
	SetDefaultDialect(d)
	return db, WrapDB(db), nil
}

// Register 注册模型并返回 Table[T]。name 为表名（空则用类型名蛇形化）。
// 默认用 DefaultDialect。
func Register[T any](name string) *meta.Table[T] {
	return meta.Register[T](name)
}

// From 返回绑定到 table、通过 db 执行的 Query[T]，使用默认方言。
func From[T any](t *meta.Table[T], db DB) *query.Query[T] {
	return query.New[T](t, DefaultDialect(), db)
}

// FromDialect 同 From，但指定方言。
func FromDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect) *query.Query[T] {
	return query.New[T](t, d, db)
}

// Insert 返回绑定到 target 实体的 Inserter（插入 target 中已 Set 的字段）。
// 无 Where 的 Update 自动按主键更新；Insert 支持 .OnConflict 做 Upsert。
func Insert[T any](t *meta.Table[T], db DB, target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, DefaultDialect(), db, target)
}

// InsertBatch 批量插入。targets 的已 Set 列取并集，缺失列填 NULL。
// RETURNING 路径逐行回填主键；MySQL 旧版只回填首个主键（文档限制）。
func InsertBatch[T any](t *meta.Table[T], db DB, targets []*T) *query.Inserter[T] {
	return query.NewInsertBatch[T](t, DefaultDialect(), db, targets)
}

// Upsert 是 Insert 的别名，语义提示用 OnConflict（INSERT...ON CONFLICT/ON DUPLICATE KEY）。
// 用法：fusion.Upsert(Users, db, &u).OnConflict([]string{"id"}, []string{"name"}).Exec(ctx)
func Upsert[T any](t *meta.Table[T], db DB, target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, DefaultDialect(), db, target)
}

// InsertDialect 同 Insert，但指定方言。
func InsertDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect, target *T) *query.Inserter[T] {
	return query.NewInsert[T](t, d, db, target)
}

// Update 返回绑定到 target 实体的 Updater（仅更新 set==true 的字段，见 #3）。
func Update[T any](t *meta.Table[T], db DB, target *T) *query.Updater[T] {
	return query.NewUpdate[T](t, DefaultDialect(), db, target)
}

// UpdateDialect 同 Update，但指定方言。
func UpdateDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect, target *T) *query.Updater[T] {
	return query.NewUpdate[T](t, d, db, target)
}

// Delete 返回 Deleter。
func Delete[T any](t *meta.Table[T], db DB) *query.Deleter[T] {
	return query.NewDelete[T](t, DefaultDialect(), db)
}

// DeleteDialect 同 Delete，但指定方言。
func DeleteDialect[T any](t *meta.Table[T], db DB, d dialect.Dialect) *query.Deleter[T] {
	return query.NewDelete[T](t, d, db)
}

// DeleteByID 按主键删除单条（无需手动 Where）。
// id 为单个标量，绑定到模型的【首个】主键列（适用于单列主键）。
// 复合主键请用 DeleteByIDs 传入"列名→值"映射。
func DeleteByID[T any](t *meta.Table[T], db DB, id any) *query.Deleter[T] {
	pkCols := t.Meta.PrimaryKeyColumns()
	if len(pkCols) == 0 {
		return query.NewDeleteByID[T](t, DefaultDialect(), db, nil)
	}
	return query.NewDeleteByID[T](t, DefaultDialect(), db, map[string]any{pkCols[0]: id})
}

// DeleteByIDs 按主键删除单条，支持复合主键。
// ids 为"主键列名 → 值"映射，如 map[string]any{"user_id": 1, "role_id": 2}。
func DeleteByIDs[T any](t *meta.Table[T], db DB, ids map[string]any) *query.Deleter[T] {
	return query.NewDeleteByID[T](t, DefaultDialect(), db, ids)
}

// Raw 执行原始 SQL，扫描进 *[]T。out 必须指向已注册模型类型的切片。
// 这是兜底机制（见设计目标：最终允许 raw 语句）。
func Raw[T any](out *[]T, ctx context.Context, db DB, sqlStr string, args ...any) error {
	t := meta.Register[T]("")
	start := time.Now()
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), Err: err})
		return fmt.Errorf("fusion: raw query: %w", err)
	}
	defer rows.Close()
	res, err := scan.All[T](rows, t.Meta)
	logging.LogQuery(ctx, logging.QueryInfo{Op: "SELECT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: int64(len(res)), Err: err})
	if err != nil {
		return err
	}
	*out = res
	return nil
}

// --- 事务（见 docs/DESIGN.md #6）---

// TxMode 事务嵌套模式（savepoint / reuse）。
type TxMode = tx.Mode

const (
	// TxModeSavepoint 嵌套用 SAVEPOINT，支持部分回滚。
	TxModeSavepoint = tx.TxModeSavepoint
	// TxModeReuse 嵌套复用外层事务。
	TxModeReuse = tx.TxModeReuse
)

// SetDefaultTxMode 设置全局默认事务模式。
func SetDefaultTxMode(m TxMode) { tx.SetDefaultMode(m) }

// Tx 在事务中执行 fn。fn 返回 nil 提交，返回 error 回滚。
// 嵌套时按默认模式（或传入 mode）处理。db 应为 *sql.DB。
// fn 中的 From/Insert/Update/Delete 若用 WrapDB 包装的 DB + 同一 ctx，自动走事务。
func Tx(ctx context.Context, db *sql.DB, fn func(ctx context.Context) error) error {
	return tx.Tx(ctx, db, tx.DefaultMode(), fn)
}

// TxWithMode 同 Tx，但指定本次事务的模式（覆盖默认）。
func TxWithMode(ctx context.Context, db *sql.DB, mode TxMode, fn func(ctx context.Context) error) error {
	return tx.Tx(ctx, db, mode, fn)
}

// TxOption 配置事务的隔离级别/只读/死锁重试（函数式选项）。
type TxOption = tx.Option

// WithIsolation 设置事务隔离级别。
func WithIsolation(level sql.IsolationLevel) TxOption {
	return func(o *tx.Options) {
		if o.TxOptions == nil {
			o.TxOptions = &sql.TxOptions{}
		}
		o.TxOptions.Isolation = level
	}
}

// WithReadOnly 设置事务为只读。
func WithReadOnly() TxOption {
	return func(o *tx.Options) {
		if o.TxOptions == nil {
			o.TxOptions = &sql.TxOptions{}
		}
		o.TxOptions.ReadOnly = true
	}
}

// WithRetry 配置死锁/序列化失败时的重试。max 为最大重试次数（不含首次）。
// base/maxDelay 为退避初始/上限（≤0 时用默认 5ms/100ms）。
// 仅顶层事务重试；fn 必须幂等。
func WithRetry(max int, base, maxDelay time.Duration) TxOption {
	return func(o *tx.Options) {
		o.RetryDeadlocks = max
		o.RetryBaseDelay = base
		o.RetryMaxDelay = maxDelay
	}
}

// TxWith 在事务中执行 fn，支持隔离级别/只读/死锁重试（函数式选项）。
// 不传选项时等价于 Tx。示例：
//
//	fusion.TxWith(ctx, db,
//	    fusion.WithIsolation(sql.LevelSerializable),
//	    fusion.WithRetry(3, 5*time.Millisecond, 100*time.Millisecond),
//	    func(ctx context.Context) error { ... })
func TxWith(ctx context.Context, db *sql.DB, fn func(ctx context.Context) error, opts ...TxOption) error {
	o := &tx.Options{}
	for _, opt := range opts {
		opt(o)
	}
	return tx.TxWithOpts(ctx, db, tx.DefaultMode(), o, fn)
}

// --- 钩子（见 docs/DESIGN.md 钩子部分）---

// HookEvent 钩子事件类型。
type HookEvent = hook.Event

const (
	BeforeCreate = hook.BeforeCreate
	AfterCreate  = hook.AfterCreate
	BeforeUpdate = hook.BeforeUpdate
	AfterUpdate  = hook.AfterUpdate
	BeforeDelete = hook.BeforeDelete
	AfterDelete  = hook.AfterDelete
)

// HookFunc 钩子回调类型。
type HookFunc = hook.Func

// OnHook 为模型注册钩子，返回注销函数。modelPtr 应为指向模型的指针（如 (*User)(nil)）。
func OnHook(modelPtr any, event HookEvent, fn HookFunc) (unregister func()) {
	return hook.Register(modelPtr, event, fn)
}

// --- 日志与查询拦截（见 docs/DESIGN.md 日志部分）---

// QueryInfo 携带一次 SQL 执行的全部信息。
type QueryInfo = logging.QueryInfo

// QueryHook 查询拦截器类型。
type QueryHook = logging.QueryHook

// SetLogger 设置全局 slog.Logger。slog 可桥接 zap/zerolog/标准库。
// 传入 nil 等同于丢弃所有日志。
// 未调用时默认为 Level=Warn 的 stderr text logger（记慢查询和错误，SQL 需 Debug 级才看）。
func SetLogger(l *slog.Logger) { logging.SetLogger(l) }

// Logger 返回当前全局 logger。
func Logger() *slog.Logger { return logging.Logger() }

// AddQueryHook 注册查询拦截器，返回注销函数。
// 每个 SQL 执行完成后调用，可拿到 QueryInfo（含 SQL/参数/耗时/RowsAffected/错误），
// 供审计/慢查询/trace。
func AddQueryHook(h QueryHook) (unregister func()) { return logging.AddQueryHook(h) }

// SetSlowThreshold 设置慢查询阈值（默认 200ms）。超过则 Warn 级记录。
func SetSlowThreshold(d time.Duration) { logging.SetSlowThreshold(d) }

// AddSensitiveColumn 追加需脱敏的列名（大小写不敏感）。
// 日志中匹配列名对应的参数值会被替换为 "***"（避免 password/token 等进日志）。
// 默认已含 password/passwd/secret/token/api_key/access_token/refresh_token/private_key/credential。
func AddSensitiveColumn(names ...string) { logging.AddSensitiveColumn(names...) }

// SetSensitiveColumns 覆盖脱敏列名集合（传 nil 清空）。
func SetSensitiveColumns(names []string) { logging.SetSensitiveColumns(names) }

// SetRedactionEnabled 开关按列脱敏（默认开）。关闭后日志原样输出参数。
func SetRedactionEnabled(enabled bool) { logging.SetRedactionEnabled(enabled) }

// --- 关联注册（见 docs/DESIGN.md 决策 5、#2、#7）---
//
// 用回调式取字段（func(u *User) any { return &u.Posts }），完全类型安全零字符串。
// 注册一次后，Preload("Posts") 即可预加载。

// HasMany 注册一对多关联（如 User→Posts）。
//   - relField:  func(u *User) any { return &u.Posts }
//   - childFK:   func(p *Post) any { return &p.UID }（子表外键）
//   - parentRef: func(u *User) any { return &u.ID }（父主键）
func HasMany(relField, childFK, parentRef any) *relation.RelMeta {
	return relation.HasMany(relField, childFK, parentRef)
}

// HasOne 注册一对一关联。
func HasOne(relField, childFK, parentRef any) *relation.RelMeta {
	return relation.HasOne(relField, childFK, parentRef)
}

// BelongsTo 注册多对一关联（如 User→Dept）。
//   - relField:  func(u *User) any { return &u.Dept }
//   - parentFK:  func(u *User) any { return &u.DeptID }（父表外键）
//   - ref:       func(d *Dept) any { return &d.ID }（引用表主键）
func BelongsTo(relField, parentFK, ref any) *relation.RelMeta {
	return relation.BelongsTo(relField, parentFK, ref)
}

// ManyToMany 注册多对多关联（如 User↔Posts 经连接表）。
//   - relField:    func(u *User) any { return &u.Posts }
//   - joinLeftFK:  func(j *UserPost) any { return &j.UserID }
//   - joinRightFK: func(j *UserPost) any { return &j.PostID }
//   - parentRef:   func(u *User) any { return &u.ID }
//   - childRef:    func(p *Post) any { return &p.ID }
func ManyToMany(relField, joinLeftFK, joinRightFK, parentRef, childRef any) *relation.RelMeta {
	return relation.ManyToMany(relField, joinLeftFK, joinRightFK, parentRef, childRef)
}

// --- 集合复合查询（UNION/INTERSECT/EXCEPT）---
//
// 各入口接收同类型 T 的多个 *Query[T]，返回 *Compound[T]，可链式 .OrderBy/.Limit/.All。
// 所有 arm 列结构须一致；ORDER/LIMIT/OFFSET 作用于整体。

// Union 构造 UNION（去重）复合查询。
func Union[T any](first *query.Query[T], others ...*query.Query[T]) *query.Compound[T] {
	return query.Union(first, others...)
}

// UnionAll 构造 UNION ALL（不去重）。
func UnionAll[T any](first *query.Query[T], others ...*query.Query[T]) *query.Compound[T] {
	return query.UnionAll(first, others...)
}

// Intersect 构造 INTERSECT。
func Intersect[T any](first *query.Query[T], others ...*query.Query[T]) *query.Compound[T] {
	return query.Intersect(first, others...)
}

// Except 构造 EXCEPT。
func Except[T any](first *query.Query[T], others ...*query.Query[T]) *query.Compound[T] {
	return query.Except(first, others...)
}

// --- 灵活 Join + 投影 + 聚合（见 docs/DESIGN.md 决策 4）---

// JOIN 类型常量。
const (
	InnerJoin = "INNER"
	LeftJoin  = "LEFT"
	RightJoin = "RIGHT"
	FullJoin  = "FULL"
)

// SelectItem 投影项类型（col.SelectItem 的别名）。
type SelectItem = col.SelectItem

// Count 聚合函数（COUNT(*) 或 COUNT(col)）。
func Count[T any](c ...col.Col[T]) col.SelectItem { return col.Count[T](c...) }

// Sum 聚合函数。
func Sum[T any](c col.Col[T]) col.SelectItem { return col.Sum[T](c) }

// Avg 聚合函数。
func Avg[T any](c col.Col[T]) col.SelectItem { return col.Avg[T](c) }

// Min 聚合函数。
func Min[T any](c col.Col[T]) col.SelectItem { return col.Min[T](c) }

// Max 聚合函数。
func Max[T any](c col.Col[T]) col.SelectItem { return col.Max[T](c) }

// --- 窗口函数（必须配 .Over(partition, order)）---
// 用法：fusion.RowNumber().Over(nil, []string{"age DESC"}).As("rn")
//       fusion.Sum(Users.Proto.Salary).Over([]string{"dept_id"}, nil).As("dept_total")

// RowNumber 窗口函数 ROW_NUMBER()。须配 .Over。
func RowNumber() col.SelectItem { return col.RowNumber() }

// Rank 窗口函数 RANK()。
func Rank() col.SelectItem { return col.Rank() }

// DenseRank 窗口函数 DENSE_RANK()。
func DenseRank() col.SelectItem { return col.DenseRank() }

// Lag 窗口函数 LAG(col)（向前 1 行）。
func Lag[T any](c col.Col[T]) col.SelectItem { return col.Lag[T](c) }

// Lead 窗口函数 LEAD(col)（向后 1 行）。
func Lead[T any](c col.Col[T]) col.SelectItem { return col.Lead[T](c) }

// --- 子查询（见 docs/DESIGN.md）---
// 子查询接受 Query 对象（实现 SubqueryProvider），自动 build 子 SQL，
// 参数并入外层，占位符自动重写，零字符串硬编码。

// SubqueryProvider 子查询提供者接口（Query 实现）。
type SubqueryProvider = expr.SubqueryProvider

// Exists 生成 EXISTS 子查询表达式。
func Exists(sub SubqueryProvider) expr.Expr { return expr.Exists(sub) }

// NotExists 生成 NOT EXISTS 子查询表达式。
func NotExists(sub SubqueryProvider) expr.Expr { return expr.NotExists(sub) }

// --- 反向迁移：数据库 schema 内省（见 docs） ---
//
// 运行时元数据路线：读数据库 → schema.Catalog（缓存），用于校验模型漂移、
// 从外键自动注册关联。不生成 .go 源码。详见 schema 包文档。

// Catalog 是数据库 schema 内省结果（表名 → Table）。
type Catalog = schema.Catalog

// SchemaTable 是单张表的数据库侧结构信息。
type SchemaTable = schema.Table

// LoadSchema 内省数据库 schema，构建 Catalog 缓存。
// tables 为空则内省当前 schema 下所有用户表。d 为方言（决定内省 SQL）。
// q 可为 *sql.DB 或 *sql.Tx（在事务内可重复读一致地内省）。
func LoadSchema(ctx context.Context, q Queryer, d dialect.Dialect, tables ...string) (*Catalog, error) {
	return schema.Load(ctx, q, d, tables...)
}

// SchemaDiff 描述一处模型与数据库 schema 的不一致。
type SchemaDiff = schema.Diff

// BindModel 比较已注册模型与数据库内省结果，返回差异列表（空=一致）。
// tab 为 fusion.Register 的返回值（如 Users），满足 meta.TableOf 接口。
func BindModel(cat *Catalog, tab meta.TableOf) []SchemaDiff {
	return schema.Bind(cat, tab)
}

// MustBind 同 BindModel，但有差异时 panic（启动期 fail-fast，抓模型/schema 漂移）。
func MustBind(cat *Catalog, tab meta.TableOf) {
	if diffs := schema.Bind(cat, tab); len(diffs) > 0 {
		panic(fmt.Sprintf("fusion: schema/model drift detected for %s:\n%s",
			tab.ModelMeta().Type.String(), formatDiffs(diffs)))
	}
}

// AutoRegisterRelations 扫描 Catalog 外键，自动注册未手动声明的关联（手动优先）。
//   - 从外键推断 belongs_to（子表）+ has_many（引用表），按命名约定匹配模型字段。
//   - 手写 relation.HasMany/BelongsTo 已注册的关联不被覆盖。
//   - DB 无外键时 no-op，完全靠手写关联。
// 注册后即可 Preload（如 posts.dept_id → depts.id，自动等价于
// fusion.BelongsTo(...)/fusion.HasMany(...)）。
func AutoRegisterRelations(cat *Catalog) {
	schema.AutoRegisterRelations(cat)
}

// formatDiffs 把 Diff 列表格式化为多行字符串。
func formatDiffs(diffs []SchemaDiff) string {
	out := ""
	for _, d := range diffs {
		out += fmt.Sprintf("  [%s] %s: %s\n", d.Kind, d.Table, d.Detail)
	}
	return out
}

// Queryer 是 LoadSchema 所需的最小查询接口（*sql.DB / *sql.Tx 满足）。
type Queryer = schema.Queryer

