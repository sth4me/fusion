package builder

import (
	"strings"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/expr"
	"github.com/sth4me/fusion/meta"
)

// InsertQuery 描述 INSERT 语句的配置。
type InsertQuery struct {
	// Cols 是要插入的列名（已注册顺序）。
	Cols []string
	// ReturningCols 是 RETURNING 子句的列（如自增主键），方言不支持时为空。
	ReturningCols []string
	// DoUpsert 为 true 时追加 ON CONFLICT/ON DUPLICATE KEY 子句。
	DoUpsert bool
	// ConflictCols 是 UPSERT 冲突目标列名（PG/SQLite）。
	ConflictCols []string
	// UpdateCols 是 UPSERT 冲突时更新的列名。
	UpdateCols []string
	// ConflictSets 是 UPSERT 冲突时的自定义表达式 SET 列表。
	// 与 UpdateCols 互斥：ConflictSets 非空时走自定义渲染（支持累加/算术），
	// 否则走 UpdateCols 的默认覆盖语义（col = excluded.col）。
	ConflictSets []UpsertSet
}

// UpsertSet 是 ON CONFLICT 自定义 SET 子句项。
//   Col   — 要更新的列名（已引用）
//   Value — 右值表达式（expr.UpsertValue）
type UpsertSet struct {
	Col   string
	Value expr.UpsertValue
}

// BuildINSERT 生成单行 INSERT 语句。
func BuildINSERT(m *meta.ModelMeta, q InsertQuery, values []any, d dialect.Dialect) (string, []any) {
	return BuildINSERTBatch(m, q, [][]any{values}, d)
}

// BuildINSERTBatch 生成多行 INSERT 语句。
// rows 是多行值，每行按 Cols 顺序对应。所有行列数必须一致。
func BuildINSERTBatch(m *meta.ModelMeta, q InsertQuery, rows [][]any, d dialect.Dialect) (string, []any) {
	r := &renderer{d: d, aliasMap: map[string]string{}}
	args := make([]any, 0, len(rows)*len(q.Cols))
	rowPh := make([]string, len(rows))
	for ri, row := range rows {
		ph := make([]string, len(q.Cols))
		for i := range q.Cols {
			ph[i] = r.NextPlaceholder()
			args = append(args, row[i])
		}
		rowPh[ri] = "(" + strings.Join(ph, ", ") + ")"
	}

	sql := "INSERT INTO " + d.QuoteTable(m.Table) +
		" (" + quoteList(q.Cols, d) + ") VALUES " + strings.Join(rowPh, ", ")

	// UPSERT 子句
	if q.DoUpsert {
		if len(q.ConflictSets) > 0 {
			// 自定义表达式路径（累加/算术等）
			sql += renderUpsertSets(r, d, quoteListRaw(q.ConflictCols, d), q.ConflictSets)
		} else {
			// 默认覆盖语义（col = excluded.col）
			sql += d.UpsertOnConflict(quoteListRaw(q.ConflictCols, d), quoteListRaw(q.UpdateCols, d))
		}
	}

	// RETURNING（仅方言支持时）
	if d.SupportsReturning() && len(q.ReturningCols) > 0 {
		sql += " RETURNING " + quoteList(q.ReturningCols, d)
	}

	// 合并 renderer 收集的参数（ConflictSets 的 Literal/RawExpr 参数，按渲染顺序追加在 INSERT 值之后）
	args = append(args, r.Args()...)
	return sql, args
}

// renderUpsertSets 渲染自定义 SET 表达式的 UPSERT 子句。
//   PG/SQLite: `ON CONFLICT (col1, col2) DO UPDATE SET "table"."a" = "table"."a" + excluded."b", ...`
//   MySQL:     `ON DUPLICATE KEY UPDATE `a` = `a` + VALUES(`b`), ...`
// Col 是裸列名，由本函数引用。渲染过程中通过 r（renderer）收集参数。
//
// 特别地，SET 右值的列引用强制保留表前缀（临时启用 keepPrefix）：
// PG 在 ON CONFLICT DO UPDATE SET 上下文中，裸列名（如 "available"）会与
// EXCLUDED."available" 形成歧义（SQLSTATE 42702）。保留 "stocks"."available"
// 形式可消除歧义——左操作数明确指向目标表，EXCLUDED.col 明确指向候选行。
func renderUpsertSets(r *renderer, d dialect.Dialect, conflictCols []string, sets []UpsertSet) string {
	target := d.ConflictTarget(conflictCols)
	// 临时启用 keepPrefix，让 expr.Column("table", "col") 渲染为 "table"."col"
	// 而非单表去前缀的 "col"。MySQL 不存在歧义但保留前缀也无害。
	prevKeepPrefix := r.keepPrefix
	r.keepPrefix = true
	setsSQL := make([]string, 0, len(sets))
	for _, s := range sets {
		// 裸列名引用 + 右值由 UpsertValue 渲染
		setsSQL = append(setsSQL, d.QuoteIdent(s.Col)+" = "+s.Value.RenderUpsert(r))
	}
	r.keepPrefix = prevKeepPrefix
	switch d.Name() {
	case "mysql":
		return " ON DUPLICATE KEY UPDATE " + strings.Join(setsSQL, ", ")
	default: // postgres / sqlite
		return " ON CONFLICT " + target + " DO UPDATE SET " + strings.Join(setsSQL, ", ")
	}
}

// UpdateQuery 描述 UPDATE 语句的配置。
type UpdateQuery struct {
	// SetCols 是要更新的列名（仅 set==true 的字段，见 #3 局部更新）。
	SetCols []string
	// Where 是更新条件。
	Where expr.Expr
}

// BuildUPDATE 生成 UPDATE 语句的 (SQL, args)。
// values 是按 SetCols 顺序对应的 SQL 值。
func BuildUPDATE(m *meta.ModelMeta, q UpdateQuery, values []any, d dialect.Dialect) (string, []any) {
	r := &renderer{d: d, aliasMap: map[string]string{}}
	sets := make([]string, len(q.SetCols))
	args := make([]any, 0, len(values)+2)
	for i := range q.SetCols {
		sets[i] = r.QuoteCol(q.SetCols[i]) + " = " + r.NextPlaceholder()
		args = append(args, values[i])
	}

	sql := "UPDATE " + d.QuoteTable(m.Table) + " SET " + strings.Join(sets, ", ")

	if !q.Where.IsZero() {
		where := q.Where.Render(r)
		if where != "" {
			sql += " WHERE " + where
		}
	}
	return sql, append(args, r.args...)
}

// DeleteQuery 描述 DELETE 语句的配置。
type DeleteQuery struct {
	Where expr.Expr
}

// BuildDELETE 生成 DELETE 语句。
func BuildDELETE(m *meta.ModelMeta, q DeleteQuery, d dialect.Dialect) (string, []any) {
	r := &renderer{d: d, aliasMap: map[string]string{}}
	sql := "DELETE FROM " + d.QuoteTable(m.Table)
	if !q.Where.IsZero() {
		where := q.Where.Render(r)
		if where != "" {
			sql += " WHERE " + where
		}
	}
	return sql, r.args
}

// quoteList 引用列名列表并用逗号连接。
func quoteList(cols []string, d dialect.Dialect) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = d.QuoteIdent(c)
	}
	return strings.Join(out, ", ")
}

// quoteListRaw 引用列名列表（用于 UPSERT 子句，列名可能已被引用）。
// 这里假设列名是原始名，统一引用。
func quoteListRaw(cols []string, d dialect.Dialect) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = d.QuoteIdent(c)
	}
	return out
}
