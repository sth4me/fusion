package builder

import (
	"strings"

	"fusion/dialect"
	"fusion/expr"
	"fusion/meta"
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
		sql += d.UpsertOnConflict(quoteListRaw(q.ConflictCols, d), quoteListRaw(q.UpdateCols, d))
	}

	// RETURNING（仅方言支持时）
	if d.SupportsReturning() && len(q.ReturningCols) > 0 {
		sql += " RETURNING " + quoteList(q.ReturningCols, d)
	}

	return sql, args
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
