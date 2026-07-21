package dialect

// Postgres 实现 PostgreSQL 方言。
type Postgres struct{}

// PostgresDialect 是默认的 PostgreSQL 方言单例（无状态，可复用）。
var PostgresDialect Dialect = Postgres{}

func (Postgres) Name() string { return "postgres" }

func (Postgres) Placeholder(i int) string { return "$" + itoa(i) }

func (Postgres) QuoteIdent(name string) string { return quote(name, '"') }

func (p Postgres) QuoteTable(name string) string { return quoteMaybeSchema(name, p) }

func (Postgres) SupportsReturning() bool { return true }

func (Postgres) UpsertOnConflict(conflictCols, updateCols []string) string {
	out := " ON CONFLICT (" + joinCSV(conflictCols) + ") DO UPDATE SET "
	sets := make([]string, 0, len(updateCols))
	for _, c := range updateCols {
		sets = append(sets, c+" = excluded."+c)
	}
	out += joinCSV(sets)
	return out
}

// ExcludedRef 渲染 PG 的 EXCLUDED 别名引用。
// excluded 是 PG 接受的特殊关系名，不需引用也不会冲突。
func (Postgres) ExcludedRef(col string) string { return "excluded." + col }

// ConflictTarget 渲染 PG 的冲突目标（已引用列名列表）。
func (Postgres) ConflictTarget(cols []string) string { return "(" + joinCSV(cols) + ")" }
