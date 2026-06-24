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
