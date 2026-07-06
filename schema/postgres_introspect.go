package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// postgresIntrospector 用 information_schema + pg_catalog 内省 PostgreSQL schema。
//
// 注意：当前 schema（search_path 中的）通过 current_schema() 取。
// 表名不做大小写归一化（PG 对未加引号的标识符折叠为小写，调用方应传小写表名）。
type postgresIntrospector struct{}

func (postgresIntrospector) ListTables(ctx context.Context, q Queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT table_name FROM information_schema.tables
WHERE table_schema = current_schema()
  AND table_type = 'BASE TABLE'
ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (postgresIntrospector) DescribeTable(ctx context.Context, q Queryer, table string) (*Table, error) {
	t := &Table{Name: table}

	// 1. 列：information_schema.columns
	//    data_type 是规范化类型名（如 INTEGER/character varying）；udt_name 给原始名。
	colRows, err := q.QueryContext(ctx, `
SELECT column_name,
       COALESCE(character_maximum_length::text, data_type) AS sql_type,
       is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = $1
ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	for colRows.Next() {
		var name, sqlType, isNull string
		var dflt sql.NullString
		if err := colRows.Scan(&name, &sqlType, &isNull, &dflt); err != nil {
			colRows.Close()
			return nil, err
		}
		c := Column{
			Name:     name,
			SQLType:  sqlType,
			Nullable: strings.EqualFold(isNull, "YES"),
		}
		if dflt.Valid {
			dd := dflt.String
			c.Default = &dd
		}
		t.Columns = append(t.Columns, c)
	}
	colRows.Close()

	// 2. 主键：pg_constraint (contype='p')
	pkRows, err := q.QueryContext(ctx, `
SELECT a.attname
FROM pg_constraint c
JOIN pg_class rel ON rel.oid = c.conrelid
JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
JOIN pg_attribute a ON a.attrelid = rel.oid AND a.attnum = ANY(c.conkey)
WHERE c.contype = 'p'
  AND nsp.nspname = current_schema()
  AND rel.relname = $1
ORDER BY array_position(c.conkey, a.attnum)`, table)
	if err != nil {
		return nil, fmt.Errorf("pk: %w", err)
	}
	for pkRows.Next() {
		var col string
		if err := pkRows.Scan(&col); err != nil {
			pkRows.Close()
			return nil, err
		}
		t.PrimaryKey = append(t.PrimaryKey, col)
	}
	pkRows.Close()

	// 3. 外键：pg_constraint (contype='f')
	fkRows, err := q.QueryContext(ctx, `
SELECT c.conname,
       ref.relname AS ref_table,
       (SELECT array_agg(a.attname ORDER BY ord)
        FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_attribute a ON a.attrelid = rel.oid AND a.attnum = k.attnum) AS cols,
       (SELECT array_agg(ra.attname ORDER BY ord)
        FROM unnest(c.confkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_attribute ra ON ra.attrelid = ref.oid AND ra.attnum = k.attnum) AS ref_cols
FROM pg_constraint c
JOIN pg_class rel ON rel.oid = c.conrelid
JOIN pg_class ref ON ref.oid = c.confrelid
JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
WHERE c.contype = 'f'
  AND nsp.nspname = current_schema()
  AND rel.relname = $1`, table)
	if err != nil {
		return nil, fmt.Errorf("fk: %w", err)
	}
	for fkRows.Next() {
		var name, refTable string
		var cols, refCols []string // pg 返回 text[]，database/sql 扫到 []byte/string 再切
		var colsArr, refColsArr string
		if err := fkRows.Scan(&name, &refTable, &colsArr, &refColsArr); err != nil {
			fkRows.Close()
			return nil, err
		}
		cols = parsePgArray(colsArr)
		refCols = parsePgArray(refColsArr)
		fk := ForeignKey{Name: name, RefTable: refTable, RefColumns: refCols}
		if len(cols) > 0 {
			fk.Column = cols[0]
		}
		// 复合外键：当前 Bind/AutoRegisterRelations 只处理单列；保留完整信息。
		t.ForeignKeys = append(t.ForeignKeys, fk)
	}
	fkRows.Close()

	// 4. 索引：pg_index（非主键索引）
	if err := collectPgIndexes(ctx, q, table, t); err != nil {
		return nil, err
	}
	return t, nil
}

// collectPgIndexes 查询非主键索引并追加到 t.Indexes。
func collectPgIndexes(ctx context.Context, q Queryer, table string, t *Table) error {
	rows, err := q.QueryContext(ctx, `
SELECT i.relname AS index_name, ix.indisunique,
       array_agg(a.attname ORDER BY k.ord) AS cols
FROM pg_index ix
JOIN pg_class tt ON tt.oid = ix.indrelid
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_namespace nsp ON nsp.oid = tt.relnamespace
JOIN unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = tt.oid AND a.attnum = k.attnum
WHERE nsp.nspname = current_schema() AND tt.relname = $1 AND NOT ix.indisprimary
GROUP BY i.relname, ix.indisunique`, table)
	if err != nil {
		return fmt.Errorf("indexes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var unique bool
		var colsArr string
		if err := rows.Scan(&name, &unique, &colsArr); err != nil {
			return err
		}
		t.Indexes = append(t.Indexes, Index{
			Name:    name,
			Columns: parsePgArray(colsArr),
			Unique:  unique,
		})
	}
	return rows.Err()
}

// parsePgArray 把 PG text[] 的文本表示（{a,b,c}）切成切片。
// 兼容驱动返回 []byte/string 两种情况（调用方已扫成 string）。
func parsePgArray(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		inner := s[1 : len(s)-1]
		if inner == "" {
			return nil
		}
		return strings.Split(inner, ",")
	}
	if s == "" || s == "NULL" {
		return nil
	}
	return []string{s}
}
