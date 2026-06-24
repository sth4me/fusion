package query

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"fusion/builder"
	"fusion/dialect"
	"fusion/expr"
	"fusion/hook"
	"fusion/meta"
)

// reflectValueElem 解引用 ptr 到 elem Value。
func reflectValueElem(ptr any) reflect.Value {
	rv := reflect.ValueOf(ptr)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	return rv
}

// Inserter 是 INSERT 语句构建器。
type Inserter[T any] struct {
	table  *meta.Table[T]
	d      dialect.Dialect
	execer queryExecer

	// 目标实体（已 Set 过要插入的值）。
	target *T
	// Upsert 配置。
	doUpsert       bool
	conflictFields []string
	updateFields   []string
}

// NewInsert 构造 Inserter。
func NewInsert[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer, target *T) *Inserter[T] {
	return &Inserter[T]{table: t, d: d, execer: execer, target: target}
}

// OnConflict 配置 UPSERT 冲突目标与更新列（字段列名）。
func (i *Inserter[T]) OnConflict(conflictCols, updateCols []string) *Inserter[T] {
	i.doUpsert = true
	i.conflictFields = conflictCols
	i.updateFields = updateCols
	return i
}

// collectSetCols 从 target 收集所有已 Set（IsSet==true）字段的列名与 SQL 值。
// 用于 Insert（插入全部已 Set 字段）和局部 Update。
func (i *Inserter[T]) collectSetCols() (cols []string, vals []any, err error) {
	entries := i.table.Meta.CollectFields(i.target)
	for _, e := range entries {
		if !e.Valuer.IsSet() {
			continue
		}
		v, verr := e.Valuer.SQLValue()
		if verr != nil {
			return nil, nil, fmt.Errorf("fusion: field %s: %w", e.Column, verr)
		}
		cols = append(cols, e.Column)
		vals = append(vals, v)
	}
	return cols, vals, nil
}

// Exec 执行 INSERT。
//   - 触发 BeforeCreate 钩子（返回 error 则中止）
//   - 方言支持 RETURNING 且有自增主键时，回填 target 的主键字段
//   - 方言不支持 RETURNING（MySQL 旧版）时，用 LastInsertId 回填
//   - 成功后触发 AfterCreate 钩子
func (i *Inserter[T]) Exec(ctx context.Context) error {
	// BeforeCreate 钩子
	if err := hook.Trigger(ctx, i.target, hook.BeforeCreate); err != nil {
		return fmt.Errorf("fusion: BeforeCreate hook: %w", err)
	}

	cols, vals, err := i.collectSetCols()
	if err != nil {
		return err
	}

	// 确定 RETURNING 列：标记为主键/自增的字段（MVP 用元数据首字段约定，简化）
	returningCols := i.returningCols()
	q := builder.InsertQuery{
		Cols:          cols,
		ReturningCols: returningCols,
		DoUpsert:      i.doUpsert,
		ConflictCols:  i.conflictFields,
		UpdateCols:    i.updateFields,
	}
	sqlStr, args := builder.BuildINSERT(i.table.Meta, q, vals, i.d)

	// 分支：RETURNING vs LastInsertId
	var execErr error
	if i.d.SupportsReturning() && len(returningCols) > 0 {
		execErr = i.execWithReturning(ctx, sqlStr, args, returningCols)
	} else {
		execErr = i.execWithLastInsertID(ctx, sqlStr, args, returningCols)
	}
	if execErr != nil {
		return execErr
	}

	// AfterCreate 钩子
	if err := hook.Trigger(ctx, i.target, hook.AfterCreate); err != nil {
		return fmt.Errorf("fusion: AfterCreate hook: %w", err)
	}
	return nil
}

// returningCols 返回需要回填的列（主键列名）。
// MVP：模型首个字段约定为主键，返回其列名。
func (i *Inserter[T]) returningCols() []string {
	if len(i.table.Meta.Fields) == 0 {
		return nil
	}
	// 跳过关联字段，取首个普通字段作为主键
	for _, f := range i.table.Meta.Fields {
		if !f.IsRelation {
			return []string{f.Column}
		}
	}
	return nil
}

// execWithReturning 执行 INSERT ... RETURNING 并回填主键。
func (i *Inserter[T]) execWithReturning(ctx context.Context, sqlStr string, args []any, retCols []string) error {
	row := i.execer.QueryRowContext(ctx, sqlStr, args...)
	if row == nil {
		return fmt.Errorf("fusion: QueryRow returned nil (sql=%s)", sqlStr)
	}
	// 扫描 RETURNING 值回填到 target 的对应字段
	dest := i.scanDestForCols(retCols)
	if err := row.Scan(dest...); err != nil {
		return fmt.Errorf("fusion: insert returning: %w (sql=%s)", err, sqlStr)
	}
	return nil
}

// execWithLastInsertID 执行 INSERT 并用 LastInsertId 回填（MySQL 旧版路径）。
func (i *Inserter[T]) execWithLastInsertID(ctx context.Context, sqlStr string, args []any, retCols []string) error {
	res, err := i.execer.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("fusion: insert: %w (sql=%s)", err, sqlStr)
	}
	if len(retCols) == 0 {
		return nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil // 无法获取自增 ID 时静默（如非自增主键）
	}
	// 把 ID 回填到 target 的主键字段
	dest := i.scanDestForCols(retCols)
	for _, d := range dest {
		if sc, ok := d.(sqlScannerLocal); ok {
			_ = sc.Scan(id)
		}
	}
	return nil
}

// sqlScannerLocal 是 sql.Scanner 的本地别名，避免导入冲突。
type sqlScannerLocal interface {
	Scan(src any) error
}

// scanDestForCols 构造目标字段指针列表，用于 Scan 回填 RETURNING/LastInsertId。
func (i *Inserter[T]) scanDestForCols(cols []string) []any {
	entries := i.table.Meta.CollectFields(i.target)
	// 建立 列名 → FieldIndex 映射
	idxByCol := make(map[string]int, len(entries))
	for _, e := range entries {
		idxByCol[e.Column] = e.FieldIndex
	}
	rv := reflectValueElem(i.target)
	dest := make([]any, 0, len(cols))
	for _, c := range cols {
		idx, ok := idxByCol[c]
		if !ok {
			continue
		}
		fv := rv.Field(idx).Addr().Interface()
		if sc, ok := fv.(sqlScannerLocal); ok {
			dest = append(dest, sc)
		}
	}
	return dest
}

// scanDestForColsByEntry 与 scanDestForCols 类似，但按 ColEntry 的 Column 映射。
// （保留旧版以兼容；当前 RETURNING 路径用 scanDestForCols）

// Updater 是 UPDATE 语句构建器（仅更新 set==true 的字段，见 #3）。
type Updater[T any] struct {
	table  *meta.Table[T]
	d      dialect.Dialect
	execer queryExecer
	target *T
	where  expr.Expr
	all    bool // 强制全字段更新（即使未 Set）
}

// NewUpdate 构造 Updater。
func NewUpdate[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer, target *T) *Updater[T] {
	return &Updater[T]{table: t, d: d, execer: execer, target: target}
}

// Where 设置更新条件。
func (u *Updater[T]) Where(e expr.Expr) *Updater[T] {
	u.where = e
	return u
}

// AllFields 强制更新所有字段（忽略 set 标志，见 #3 全量更新 opt）。
func (u *Updater[T]) AllFields() *Updater[T] {
	u.all = true
	return u
}

// Exec 执行 UPDATE（触发 BeforeUpdate/AfterUpdate 钩子）。
func (u *Updater[T]) Exec(ctx context.Context) error {
	// BeforeUpdate 钩子
	if err := hook.Trigger(ctx, u.target, hook.BeforeUpdate); err != nil {
		return fmt.Errorf("fusion: BeforeUpdate hook: %w", err)
	}

	var cols []string
	var vals []any
	entries := u.table.Meta.CollectFields(u.target)
	for _, e := range entries {
		// 主键字段不参与更新（避免改主键）
		if isPrimaryKey(u.table.Meta, e.Column) {
			continue
		}
		if !u.all && !e.Valuer.IsSet() {
			continue // 局部更新：跳过未 Set 的字段
		}
		v, err := e.Valuer.SQLValue()
		if err != nil {
			return fmt.Errorf("fusion: field %s: %w", e.Column, err)
		}
		cols = append(cols, e.Column)
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return fmt.Errorf("fusion: update with no fields (did you Set any field?)")
	}

	sqlStr, args := builder.BuildUPDATE(u.table.Meta, builder.UpdateQuery{
		SetCols: cols,
		Where:   u.where,
	}, vals, u.d)
	_, err := u.execer.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("fusion: update: %w (sql=%s)", err, sqlStr)
	}

	// AfterUpdate 钩子
	if err := hook.Trigger(ctx, u.target, hook.AfterUpdate); err != nil {
		return fmt.Errorf("fusion: AfterUpdate hook: %w", err)
	}
	return nil
}

// isPrimaryKey 判断列名是否为主键（MVP：首个非关联字段）。
func isPrimaryKey(m *meta.ModelMeta, col string) bool {
	for _, f := range m.Fields {
		if f.IsRelation {
			continue
		}
		return f.Column == col
	}
	return false
}

// Deleter 是 DELETE 语句构建器。
type Deleter[T any] struct {
	table  *meta.Table[T]
	d      dialect.Dialect
	execer queryExecer
	where  expr.Expr
}

// NewDelete 构造 Deleter。
func NewDelete[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer) *Deleter[T] {
	return &Deleter[T]{table: t, d: d, execer: execer}
}

// Where 设置删除条件。
func (d *Deleter[T]) Where(e expr.Expr) *Deleter[T] {
	d.where = e
	return d
}

// Exec 执行 DELETE（触发 BeforeDelete/AfterDelete 钩子）。
// 注意：Delete 无具体 target 实体，钩子的 target 参数为 nil；用户可在钩子内
// 用 ctx 自行查询受影响行。
func (d *Deleter[T]) Exec(ctx context.Context) error {
	// BeforeDelete 钩子（无实例，按模型类型触发，target=nil）
	if err := hook.TriggerByType(ctx, d.table.Meta.Type, hook.BeforeDelete, nil); err != nil {
		return fmt.Errorf("fusion: BeforeDelete hook: %w", err)
	}

	sqlStr, args := builder.BuildDELETE(d.table.Meta, builder.DeleteQuery{
		Where: d.where,
	}, d.d)
	_, err := d.execer.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("fusion: delete: %w (sql=%s)", err, sqlStr)
	}

	// AfterDelete 钩子
	if err := hook.TriggerByType(ctx, d.table.Meta.Type, hook.AfterDelete, nil); err != nil {
		return fmt.Errorf("fusion: AfterDelete hook: %w", err)
	}
	return nil
}

// 确保导入使用
var _ = sql.ErrNoRows
