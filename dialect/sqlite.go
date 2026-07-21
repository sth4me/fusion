package dialect

// SQLite 实现 SQLite 方言（3.35+ 支持 RETURNING）。
type SQLite struct{}

// SQLiteDialect 是默认 SQLite 方言单例。
var SQLiteDialect Dialect = SQLite{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) Placeholder(int) string { return "?" } // SQLite 也支持 $1，统一用 ?

func (SQLite) QuoteIdent(name string) string { return quote(name, '"') }

func (s SQLite) QuoteTable(name string) string { return quoteMaybeSchema(name, s) }

func (SQLite) SupportsReturning() bool { return true }

func (SQLite) UpsertOnConflict(conflictCols, updateCols []string) string {
	// SQLite 语法与 PostgreSQL 一致（ON CONFLICT ... DO UPDATE SET ...=excluded....）
	out := " ON CONFLICT (" + joinCSV(conflictCols) + ") DO UPDATE SET "
	sets := make([]string, 0, len(updateCols))
	for _, c := range updateCols {
		sets = append(sets, c+" = excluded."+c)
	}
	out += joinCSV(sets)
	return out
}

// ExcludedRef 渲染 SQLite 的 excluded 别名引用（语法同 PG）。
func (SQLite) ExcludedRef(col string) string { return "excluded." + col }

// ConflictTarget 渲染 SQLite 的冲突目标（语法同 PG）。
func (SQLite) ConflictTarget(cols []string) string { return "(" + joinCSV(cols) + ")" }
