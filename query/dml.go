package query

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"time"

	"github.com/sth4me/fusion/builder"
	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/hook"
	"github.com/sth4me/fusion/logging"
	"github.com/sth4me/fusion/meta"
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

	// 单条插入目标。
	target *T
	// 批量插入目标（非空时走批量路径）。
	targets []*T
	// Upsert 配置。
	doUpsert       bool
	conflictFields []string
	updateFields   []string
}

// NewInsert 构造单条 Inserter。
func NewInsert[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer, target *T) *Inserter[T] {
	return &Inserter[T]{table: t, d: d, execer: execer, target: target}
}

// NewInsertBatch 构造批量 Inserter。
func NewInsertBatch[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer, targets []*T) *Inserter[T] {
	return &Inserter[T]{table: t, d: d, execer: execer, targets: targets}
}

// Batch 设置批量目标（覆盖单条 target）。
func (i *Inserter[T]) Batch(targets []*T) *Inserter[T] {
	i.targets = targets
	i.target = nil
	return i
}

// OnConflict 配置 UPSERT 冲突目标与更新列（字段列名）。
func (i *Inserter[T]) OnConflict(conflictCols, updateCols []string) *Inserter[T] {
	i.doUpsert = true
	i.conflictFields = conflictCols
	i.updateFields = updateCols
	return i
}

// collectSetCols 从 target 收集所有已 Set（IsSet==true）字段的列名与 SQL 值。
func (i *Inserter[T]) collectSetCols(target *T) (cols []string, vals []any, err error) {
	entries := i.table.Meta.CollectFields(target)
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

// unionBatchCols 取所有 batch target 的 set 列并集（按首次出现顺序）。
func (i *Inserter[T]) unionBatchCols() []string {
	seen := map[string]bool{}
	var cols []string
	for _, t := range i.targets {
		entries := i.table.Meta.CollectFields(t)
		for _, e := range entries {
			if e.Valuer.IsSet() && !seen[e.Column] {
				seen[e.Column] = true
				cols = append(cols, e.Column)
			}
		}
	}
	return cols
}

// collectRowAligned 按 cols 顺序收集单个 target 的值（缺失列填 nil）。
func (i *Inserter[T]) collectRowAligned(target *T, cols []string) []any {
	entries := i.table.Meta.CollectFields(target)
	byCol := map[string]any{}
	for _, e := range entries {
		if e.Valuer.IsSet() {
			v, _ := e.Valuer.SQLValue()
			byCol[e.Column] = v
		}
	}
	row := make([]any, len(cols))
	for j, c := range cols {
		if v, ok := byCol[c]; ok {
			row[j] = v
		} else {
			row[j] = nil
		}
	}
	return row
}

// execBatchReturning 批量 INSERT RETURNING，扫描多行回填主键到各 target。
func (i *Inserter[T]) execBatchReturning(ctx context.Context, sqlStr string, args []any, retCols []string) (int64, error) {
	rows, err := i.execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("fusion: batch insert: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	// 读 RETURNING 主键列，按行序回填各 target
	pkCol := ""
	if len(retCols) > 0 {
		pkCol = retCols[0]
	}
	n := int64(0)
	ti := 0
	for rows.Next() {
		if ti >= len(i.targets) {
			break
		}
		// 扫描主键值回填
		dest := i.scanDestForColsTarget(i.targets[ti], retCols)
		if err := rows.Scan(dest...); err != nil {
			return n, fmt.Errorf("fusion: batch insert returning: %w", err)
		}
		n++
		ti++
	}
	_ = pkCol
	return n, rows.Err()
}

// execBatchLastInsertID 批量 INSERT 无 RETURNING（MySQL 旧版）。
// MySQL LastInsertId 只返回首个 ID，批量场景仅回填首个 target（文档限制）。
func (i *Inserter[T]) execBatchLastInsertID(ctx context.Context, sqlStr string, args []any) (int64, error) {
	res, err := i.execer.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("fusion: batch insert: %w (sql=%s)", err, sqlStr)
	}
	n, _ := res.RowsAffected()
	if len(i.targets) > 0 {
		id, err := res.LastInsertId()
		if err == nil && len(i.returningCols()) > 0 {
			// 只回填首个 target（MySQL 限制）
			dest := i.scanDestForColsTarget(i.targets[0], i.returningCols())
			for _, d := range dest {
				if sc, ok := d.(sqlScannerLocal); ok {
					_ = sc.Scan(id)
				}
			}
		}
	}
	return n, nil
}

// Exec 执行 INSERT（单条或批量，取决于 target/targets）。
func (i *Inserter[T]) Exec(ctx context.Context) error {
	if len(i.targets) > 0 {
		return i.execBatch(ctx)
	}
	return i.execSingle(ctx)
}

// execSingle 单条插入。
func (i *Inserter[T]) execSingle(ctx context.Context) error {
	// BeforeCreate 钩子
	if err := hook.Trigger(ctx, i.target, hook.BeforeCreate); err != nil {
		return fmt.Errorf("fusion: BeforeCreate hook: %w", err)
	}

	cols, vals, err := i.collectSetCols(i.target)
	if err != nil {
		return err
	}

	returningCols := i.returningCols()
	q := builder.InsertQuery{
		Cols:          cols,
		ReturningCols: returningCols,
		DoUpsert:      i.doUpsert,
		ConflictCols:  i.conflictFields,
		UpdateCols:    i.updateFields,
	}
	sqlStr, args := builder.BuildINSERT(i.table.Meta, q, vals, i.d)

	start := time.Now()
	var rowsAffected int64
	var execErr error
	if i.d.SupportsReturning() && len(returningCols) > 0 {
		rowsAffected, execErr = i.execWithReturning(ctx, sqlStr, args, returningCols)
	} else {
		rowsAffected, execErr = i.execWithLastInsertID(ctx, sqlStr, args, returningCols)
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: "INSERT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsAffected, Err: execErr})
	if execErr != nil {
		return execErr
	}

	// AfterCreate 钩子
	if err := hook.Trigger(ctx, i.target, hook.AfterCreate); err != nil {
		return fmt.Errorf("fusion: AfterCreate hook: %w", err)
	}
	return nil
}

// execBatch 批量插入。
func (i *Inserter[T]) execBatch(ctx context.Context) error {
	// BeforeCreate 钩子逐条触发
	for _, t := range i.targets {
		if err := hook.Trigger(ctx, t, hook.BeforeCreate); err != nil {
			return fmt.Errorf("fusion: BeforeCreate hook: %w", err)
		}
	}

	// 收集每行的 set 列（取所有行的列并集；每行按列填值，缺失填 nil）
	cols := i.unionBatchCols()
	rows := make([][]any, 0, len(i.targets))
	for _, t := range i.targets {
		row := i.collectRowAligned(t, cols)
		rows = append(rows, row)
	}

	returningCols := i.returningCols()

	// MySQL 等无 RETURNING 的方言：批量插入只能拿 LastInsertId（仅首行主键），
	// 多行时其余行主键无法回填。为正确性，此时退化为逐行插入（每行独立 LastInsertId）。
	// RETURNING 方言（PG/SQLite）走批量 RETURNING 路径，不受影响。
	if !i.d.SupportsReturning() && len(returningCols) > 0 && len(i.targets) > 1 {
		return i.execBatchRowByRow(ctx)
	}

	q := builder.InsertQuery{
		Cols:          cols,
		ReturningCols: returningCols,
		DoUpsert:      i.doUpsert,
		ConflictCols:  i.conflictFields,
		UpdateCols:    i.updateFields,
	}
	sqlStr, args := builder.BuildINSERTBatch(i.table.Meta, q, rows, i.d)

	start := time.Now()
	var rowsAffected int64
	var execErr error
	if i.d.SupportsReturning() && len(returningCols) > 0 {
		rowsAffected, execErr = i.execBatchReturning(ctx, sqlStr, args, returningCols)
	} else {
		rowsAffected, execErr = i.execBatchLastInsertID(ctx, sqlStr, args)
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: "INSERT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsAffected, Err: execErr})
	if execErr != nil {
		return execErr
	}

	// AfterCreate 钩子逐条触发
	for _, t := range i.targets {
		if err := hook.Trigger(ctx, t, hook.AfterCreate); err != nil {
			return fmt.Errorf("fusion: AfterCreate hook: %w", err)
		}
	}
	return nil
}

// execBatchRowByRow 在无 RETURNING 的方言（MySQL）上逐行插入，正确回填每行主键。
// 由 execBatch 在 !SupportsReturning() && 多行时调用。每行独立 INSERT + LastInsertId。
// 性能低于批量，但保证每行的自增主键正确回填（避免 C3：批量只回填首行）。
func (i *Inserter[T]) execBatchRowByRow(ctx context.Context) error {
	returningCols := i.returningCols()
	for _, t := range i.targets {
		cols, vals, err := i.collectSetCols(t)
		if err != nil {
			return err
		}
		q := builder.InsertQuery{
			Cols:          cols,
			ReturningCols: returningCols,
			DoUpsert:      i.doUpsert,
			ConflictCols:  i.conflictFields,
			UpdateCols:    i.updateFields,
		}
		sqlStr, args := builder.BuildINSERT(i.table.Meta, q, vals, i.d)
		start := time.Now()
		res, execErr := i.execer.ExecContext(ctx, sqlStr, args...)
		var rowsAffected int64
		if execErr == nil && res != nil {
			rowsAffected, _ = res.RowsAffected()
		}
		logging.LogQuery(ctx, logging.QueryInfo{Op: "INSERT", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsAffected, Err: execErr})
		if execErr != nil {
			return fmt.Errorf("fusion: insert: %w (sql=%s)", execErr, sqlStr)
		}
		// 回填主键（LastInsertId）
		if len(returningCols) > 0 && res != nil {
			if id, e := res.LastInsertId(); e == nil {
				dest := i.scanDestForColsTarget(t, returningCols)
				for _, d := range dest {
					if sc, ok := d.(sqlScannerLocal); ok {
						_ = sc.Scan(id)
					}
				}
			}
		}
		// AfterCreate 逐行触发（与批量路径语义一致）
		if err := hook.Trigger(ctx, t, hook.AfterCreate); err != nil {
			return fmt.Errorf("fusion: AfterCreate hook: %w", err)
		}
	}
	return nil
}


// returningCols 返回需要回填的主键列（复合主键时返回多列；RETURNING 多列扫描已支持）。
// MySQL LastInsertId 路径仅回填首个主键（已知限制）。
func (i *Inserter[T]) returningCols() []string {
	if len(i.table.Meta.Fields) == 0 {
		return nil
	}
	return i.table.Meta.PrimaryKeyColumns()
}

// execWithReturning 执行 INSERT ... RETURNING 并回填主键。
// 返回受影响行数（RETURNING 路径固定为 1）。
func (i *Inserter[T]) execWithReturning(ctx context.Context, sqlStr string, args []any, retCols []string) (int64, error) {
	row := i.execer.QueryRowContext(ctx, sqlStr, args...)
	if row == nil {
		return 0, fmt.Errorf("fusion: QueryRow returned nil (sql=%s)", sqlStr)
	}
	// 扫描 RETURNING 值回填到 target 的对应字段
	dest := i.scanDestForCols(retCols)
	if err := row.Scan(dest...); err != nil {
		return 0, fmt.Errorf("fusion: insert returning: %w (sql=%s)", err, sqlStr)
	}
	return 1, nil
}

// execWithLastInsertID 执行 INSERT 并用 LastInsertId 回填（MySQL 旧版路径）。
// 返回受影响行数（从 sql.Result 取）。
func (i *Inserter[T]) execWithLastInsertID(ctx context.Context, sqlStr string, args []any, retCols []string) (int64, error) {
	res, err := i.execer.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("fusion: insert: %w (sql=%s)", err, sqlStr)
	}
	rows, _ := res.RowsAffected()
	if len(retCols) == 0 {
		return rows, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return rows, nil // 无法获取自增 ID 时静默（如非自增主键）
	}
	// 把 ID 回填到 target 的主键字段
	dest := i.scanDestForCols(retCols)
	for _, d := range dest {
		if sc, ok := d.(sqlScannerLocal); ok {
			_ = sc.Scan(id)
		}
	}
	return rows, nil
}

// sqlScannerLocal 是 sql.Scanner 的本地别名，避免导入冲突。
type sqlScannerLocal interface {
	Scan(src any) error
}

// scanDestForCols 构造目标字段指针列表，用于 Scan 回填 RETURNING/LastInsertId。
func (i *Inserter[T]) scanDestForCols(cols []string) []any {
	return i.scanDestForColsTarget(i.target, cols)
}

// scanDestForColsTarget 对指定 target 构造字段指针列表（批量回填用）。
func (i *Inserter[T]) scanDestForColsTarget(target *T, cols []string) []any {
	entries := i.table.Meta.CollectFields(target)
	idxByCol := make(map[string]int, len(entries))
	for _, e := range entries {
		idxByCol[e.Column] = e.FieldIndex
	}
	rv := reflectValueElem(target)
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

// buildPKWhere 用 target 的所有主键值构造 WHERE（pk1=? AND pk2=? ...）。
// 支持复合主键；单列主键时退化为 pk=?。
func (u *Updater[T]) buildPKWhere() (expr.Expr, error) {
	pkCols := u.table.Meta.PrimaryKeyColumns()
	if len(pkCols) == 0 {
		return expr.Expr{}, fmt.Errorf("fusion: update without Where requires a primary key")
	}
	entries := u.table.Meta.CollectFields(u.target)
	// 列名 → SQL 值
	valByCol := map[string]any{}
	for _, e := range entries {
		valByCol[e.Column] = nil
		v, err := e.Valuer.SQLValue()
		if err != nil {
			return expr.Expr{}, err
		}
		valByCol[e.Column] = v
	}
	var conds []expr.Expr
	for _, pk := range pkCols {
		v, ok := valByCol[pk]
		if !ok {
			return expr.Expr{}, fmt.Errorf("fusion: primary key %s not found on target", pk)
		}
		conds = append(conds, expr.LeafParam(pk, "=", v))
	}
	// 从零值 Expr 起 And 所有 PK 条件（单列退化为一项，多列拼成 pk1=? AND pk2=?）
	var where expr.Expr
	for _, c := range conds {
		where = where.And(c)
	}
	return where, nil
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

	// 若未设 Where，自动用 target 主键构造（便捷：Update(t,db,&u).Exec 按主键更新）
	where := u.where
	if where.IsZero() {
		pkWhere, err := u.buildPKWhere()
		if err != nil {
			return err
		}
		where = pkWhere
	}

	sqlStr, args := builder.BuildUPDATE(u.table.Meta, builder.UpdateQuery{
		SetCols: cols,
		Where:   where,
	}, vals, u.d)
	start := time.Now()
	res, err := u.execer.ExecContext(ctx, sqlStr, args...)
	var rowsAffected int64
	if err == nil && res != nil {
		rowsAffected, _ = res.RowsAffected()
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: "UPDATE", SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsAffected, Err: err})
	if err != nil {
		return fmt.Errorf("fusion: update: %w (sql=%s)", err, sqlStr)
	}

	// AfterUpdate 钩子
	if err := hook.Trigger(ctx, u.target, hook.AfterUpdate); err != nil {
		return fmt.Errorf("fusion: AfterUpdate hook: %w", err)
	}
	return nil
}

// isPrimaryKey 判断列名是否为主键列（复合主键时多个列都算）。
// 遍历所有 IsPrimaryKey 字段，而非只看首个非关联字段（修掉旧 bug）。
func isPrimaryKey(m *meta.ModelMeta, col string) bool {
	for _, f := range m.Fields {
		if f.IsPrimaryKey && f.Column == col {
			return true
		}
	}
	return false
}

// primaryKeyColumn 返回模型的【首个】主键列名（向后兼容；复合主键请用 PrimaryKeyColumns）。
// 无主键返回空串。
func primaryKeyColumn(m *meta.ModelMeta) string {
	cols := m.PrimaryKeyColumns()
	if len(cols) == 0 {
		return ""
	}
	return cols[0]
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

// NewDeleteByID 构造按主键删除的 Deleter（无需手动 Where）。
//
// id 按"列名 → 值"映射传入，支持复合主键：
//   - 单列主键：NewDeleteByID(t, d, ex, map[string]any{"id": 1})
//   - 复合主键：NewDeleteByID(t, d, ex, map[string]any{"user_id": 1, "role_id": 2})
//
// 若只关心单列主键，fusion 层提供便捷 DeleteByID(t, db, id) —— 等价于
// 用模型首个主键列名包成单元素 map。
func NewDeleteByID[T any](t *meta.Table[T], d dialect.Dialect, execer queryExecer, id map[string]any) *Deleter[T] {
	pkCols := t.Meta.PrimaryKeyColumns()
	var conds []expr.Expr
	for _, pk := range pkCols {
		if v, ok := id[pk]; ok {
			conds = append(conds, expr.LeafParam(pk, "=", v))
		}
	}
	var where expr.Expr
	for _, c := range conds {
		where = where.And(c)
	}
	return &Deleter[T]{table: t, d: d, execer: execer, where: where}
}

// Where 设置删除条件。
func (d *Deleter[T]) Where(e expr.Expr) *Deleter[T] {
	d.where = e
	return d
}

// Exec 执行 DELETE（触发 BeforeDelete/AfterDelete 钩子）。
//
// 若模型声明了 col.SoftDelete 字段，自动改写为 UPDATE SET deleted_at = now()
// （软删除），并自动追加 WHERE deleted_at IS NULL（只删未删的行）。
// 触发的仍是 BeforeDelete/AfterDelete 钩子（语义一致：用户调的是 Delete）。
//
// 注意：Delete 无具体 target 实体，钩子的 target 参数为 nil；用户可在钩子内
// 用 ctx 自行查询受影响行。
func (d *Deleter[T]) Exec(ctx context.Context) error {
	// BeforeDelete 钩子（无实例，按模型类型触发，target=nil）
	if err := hook.TriggerByType(ctx, d.table.Meta.Type, hook.BeforeDelete, nil); err != nil {
		return fmt.Errorf("fusion: BeforeDelete hook: %w", err)
	}

	sdCol := d.table.Meta.SoftDeleteColumn()
	where := d.where
	var sqlStr string
	var args []any
	op := "DELETE"

	if sdCol != "" {
		// 软删除：UPDATE SET deleted_at = now() WHERE ... AND deleted_at IS NULL
		sdFilter := expr.LeafRaw(d.table.Meta.Table+"."+sdCol, "IS NULL")
		where = where.And(sdFilter)
		now := time.Now()
		sqlStr, args = builder.BuildUPDATE(d.table.Meta, builder.UpdateQuery{
			SetCols: []string{sdCol},
			Where:   where,
		}, []any{now}, d.d)
		op = "DELETE(soft)" // 日志标识软删除
	} else {
		// 硬删除：DELETE FROM ... WHERE ...
		sqlStr, args = builder.BuildDELETE(d.table.Meta, builder.DeleteQuery{
			Where: where,
		}, d.d)
	}

	start := time.Now()
	res, err := d.execer.ExecContext(ctx, sqlStr, args...)
	var rowsAffected int64
	if err == nil && res != nil {
		rowsAffected, _ = res.RowsAffected()
	}
	logging.LogQuery(ctx, logging.QueryInfo{Op: op, SQL: sqlStr, Args: args, Duration: time.Since(start), RowsAffected: rowsAffected, Err: err})
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
