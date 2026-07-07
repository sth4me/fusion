package dialect

// MySQL 实现现代 MySQL 方言。
//
// 关于 RETURNING：MySQL 8.0+ 与 MariaDB 10.5+ 支持 INSERT/UPDATE/DELETE ... RETURNING。
// 旧版不支持。默认单例 MySQLDialect 设 SupportsRet=false（兼容旧版），导致：
//   - 单条插入：走 LastInsertId 回填自增主键（正常）
//   - 批量插入：自动退化为逐行插入（保证每行主键正确回填，见 query.execBatchRowByRow）
//
// 用 MySQL 8.0+/MariaDB 10.5+ 时，建议用 NewMySQL(true) 启用 RETURNING（批量插入性能更好）。
type MySQL struct {
	// SupportsRet 控制 SupportsReturning 返回值；默认 false（兼容旧版），
	// 用 MySQL 8.0+/MariaDB 10.5+ 时设 true。
	SupportsRet bool
}

// MySQLDialect 是默认 MySQL 方言单例（旧版，无 RETURNING）。
var MySQLDialect Dialect = &MySQL{SupportsRet: false}

// NewMySQL 构造 MySQL 方言，supportsReturning 控制是否启用 RETURNING。
// MySQL 8.0+/MariaDB 10.5+ 传 true（批量插入走 RETURNING，性能优于逐行退化路径）。
func NewMySQL(supportsReturning bool) *MySQL {
	return &MySQL{SupportsRet: supportsReturning}
}

func (*MySQL) Name() string { return "mysql" }

func (*MySQL) Placeholder(int) string { return "?" }

func (*MySQL) QuoteIdent(name string) string { return quote(name, '`') }

func (m *MySQL) QuoteTable(name string) string { return quoteMaybeSchema(name, m) }

func (m *MySQL) SupportsReturning() bool { return m.SupportsRet }

func (*MySQL) UpsertOnConflict(conflictCols, updateCols []string) string {
	// MySQL 的 ON DUPLICATE KEY 不显式指定冲突列（靠唯一键/主键自动判定），
	// 故 conflictCols 被忽略，仅渲染更新子句。
	out := " ON DUPLICATE KEY UPDATE "
	sets := make([]string, 0, len(updateCols))
	for _, c := range updateCols {
		sets = append(sets, c+" = VALUES("+c+")")
	}
	out += joinCSV(sets)
	return out
}
