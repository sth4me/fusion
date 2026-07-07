package dialect

// MySQL 实现现代 MySQL 方言。
//
// 关于 RETURNING：MySQL 全系列（含 8.0）**不支持** INSERT/UPDATE/DELETE ... RETURNING。
// RETURNING 是 MariaDB 10.5+ 的特性。因此：
//   - MySQL（任意版本）：用 NewMySQL(false) 或默认 MySQLDialect，走 LastInsertId 回填；
//     批量插入自动退化为逐行（execBatchRowByRow），保证每行主键正确回填。
//   - MariaDB 10.5+：用 NewMySQL(true) 启用 RETURNING（批量插入性能更好）。
type MySQL struct {
	// SupportsRet 控制 SupportsReturning 返回值；默认 false（兼容 MySQL 全系列）。
	// 仅 MariaDB 10.5+ 才应设 true（MySQL 8.0 不支持 RETURNING，设 true 会语法错误）。
	SupportsRet bool
}

// MySQLDialect 是默认 MySQL 方言单例（MySQL 全系列，无 RETURNING）。
var MySQLDialect Dialect = &MySQL{SupportsRet: false}

// NewMySQL 构造 MySQL 方言，supportsReturning 控制是否启用 RETURNING。
// MySQL（任意版本）传 false；MariaDB 10.5+ 传 true。
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
