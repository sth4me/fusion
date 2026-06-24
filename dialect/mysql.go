package dialect

// MySQL 实现现代 MySQL 方言。
// 旧版（< 8.0 / 非 MariaDB 10.5+）不支持 RETURNING，SupportsReturning 返回 false，
// 由 query 层退化为二次 SELECT。
type MySQL struct {
	// SupportsRet 控制 SupportsReturning 返回值；默认 false（兼容旧版），
	// 用 MySQL 8.0+/MariaDB 10.5+ 时可设 true。
	SupportsRet bool
}

// MySQLDialect 是默认 MySQL 方言单例（旧版，无 RETURNING）。
var MySQLDialect Dialect = &MySQL{SupportsRet: false}

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
