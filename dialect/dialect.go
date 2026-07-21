// Package dialect 抽象不同数据库方言的差异。
//
// 详见 docs/DESIGN.md #4：API 层统一，Dialect 抹平底层差异（占位符、RETURNING、
// UPSERT 语法）。
package dialect

// Dialect 抽象数据库方言。所有 SQL 生成最终通过 Dialect 渲染。
type Dialect interface {
	// Name 返回方言名（postgres / mysql / sqlite），用于日志与错误信息。
	Name() string

	// Placeholder 返回第 i 个占位符（i 从 1 开始）。
	// PostgreSQL: "$1"；MySQL/SQLite: "?"。
	Placeholder(i int) string

	// QuoteIdent 引用标识符（表名/列名），防止与关键字冲突并处理大小写。
	// PostgreSQL/SQLite: "name"；MySQL: `name`。
	QuoteIdent(name string) string

	// QuoteTable 引用表名（可含 schema 前缀），内部用 QuoteIdent。
	QuoteTable(name string) string

	// SupportsReturning 报告该方言是否支持 INSERT/UPDATE/DELETE ... RETURNING。
	// PostgreSQL/SQLite(3.35+): true；MySQL 旧版: false（退化为二次 SELECT）。
	SupportsReturning() bool

	// UpsertOnConflict 渲染 UPSERT 的冲突子句。
	// conflictCols 为冲突目标列名（已引用），updateCols 为冲突时更新的列名（已引用）。
	//   PostgreSQL/SQLite: ON CONFLICT (...) DO UPDATE SET ...=excluded....
	//   MySQL: ON DUPLICATE KEY UPDATE ...=VALUES(...)
	// 返回冲突子句 SQL（不含前面的 INSERT 部分）与对应参数（多数方言无额外参数）。
	UpsertOnConflict(conflictCols, updateCols []string) string

	// ExcludedRef 引用 UPSERT 场景下"插入行中的列值"（冲突时的候选新值）。
	//   PostgreSQL/SQLite: EXCLUDED."col"（小写关键字 excluded 是 PG/SQLite 接受的别名）
	//   MySQL: VALUES(`col`)
	// 供 OnConflictSet 自定义表达式（累加/算术）使用。
	ExcludedRef(col string) string

	// ConflictTarget 渲染 UPSERT 的冲突目标子句（ON CONFLICT 之后、DO 之前的部分）。
	//   PostgreSQL/SQLite: ("col1", "col2")
	//   MySQL: ""（ON DUPLICATE KEY 不显式指定冲突列，靠唯一键自动判定）
	ConflictTarget(cols []string) string
}

// EscapeBytes 转义字符串字面量内的单引号（用于 raw SQL，慎用）。
func EscapeBytes(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
