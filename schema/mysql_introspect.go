package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// mysqlIntrospector 用 information_schema 内省 MySQL/MariaDB schema。
//
// 表名大小写敏感度取决于 lower_case_table_names；调用方应传实际存储名。
// schema（database）通过 DATABASE() 取当前库。
type mysqlIntrospector struct{}

func (mysqlIntrospector) ListTables(ctx context.Context, q Queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
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

func (mysqlIntrospector) DescribeTable(ctx context.Context, q Queryer, table string) (*Table, error) {
	t := &Table{Name: table}

	// 1. 列：information_schema.columns
	//    column_type 给完整类型（如 varchar(255)/int unsigned），DATA_TYPE 是简名。
	colRows, err := q.QueryContext(ctx, `
SELECT column_name, column_type, is_nullable, column_default, column_key
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	for colRows.Next() {
		var name, sqlType, isNull string
		var dflt sql.NullString
		var colKey string
		if err := colRows.Scan(&name, &sqlType, &isNull, &dflt, &colKey); err != nil {
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
		// MySQL 单列主键由 column_key='PRI' 标识；复合主键多行都是 PRI，
		// 需用下面 key_column_usage 按序还原。这里先记单列场景。
		if strings.EqualFold(colKey, "PRI") {
			// 仅当目前只有 0 或 1 个 PK 列时追加（避免与 key_column_usage 重复）
			if len(t.PrimaryKey) <= 1 {
				if len(t.PrimaryKey) == 0 {
					t.PrimaryKey = []string{name}
				} else {
					// 已有一个，说明可能复合；清空交给 key_column_usage
					t.PrimaryKey = nil
				}
			}
		}
	}
	colRows.Close()

	// 2. 主键：用 key_column_usage 按 ordinal_position 还原（保证复合主键顺序正确）
	pkRows, err := q.QueryContext(ctx, `
SELECT kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.constraint_type = 'PRIMARY KEY'
  AND tc.table_schema = DATABASE() AND tc.table_name = ?
ORDER BY kcu.ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("pk: %w", err)
	}
	var pkCols []string
	for pkRows.Next() {
		var col string
		if err := pkRows.Scan(&col); err != nil {
			pkRows.Close()
			return nil, err
		}
		pkCols = append(pkCols, col)
	}
	pkRows.Close()
	if len(pkCols) > 0 {
		t.PrimaryKey = pkCols // 覆盖上面从 column_key 的推断
	}

	// 3. 外键：information_schema.referential_constraints + key_column_usage
	fkRows, err := q.QueryContext(ctx, `
SELECT rc.constraint_name,
       rc.referenced_table_name,
       kcu.column_name,
       kcu.referenced_column_name,
       kcu.ordinal_position
FROM information_schema.referential_constraints rc
JOIN information_schema.key_column_usage kcu
  ON rc.constraint_name = kcu.constraint_name
 AND rc.table_schema = kcu.table_schema
 AND rc.table_name = kcu.table_name
WHERE rc.constraint_schema = DATABASE() AND rc.table_name = ?
ORDER BY rc.constraint_name, kcu.ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("fk: %w", err)
	}
	type fkPart struct {
		name, refTable, col, refCol string
	}
	var parts []fkPart
	for fkRows.Next() {
		var name, refTable, col, refCol string
		var ord int
		if err := fkRows.Scan(&name, &refTable, &col, &refCol, &ord); err != nil {
			fkRows.Close()
			return nil, err
		}
		parts = append(parts, fkPart{name, refTable, col, refCol})
	}
	fkRows.Close()
	// 按 constraint_name 分组（复合外键场景）
	groups := map[string][]fkPart{}
	var order []string
	for _, p := range parts {
		if _, ok := groups[p.name]; !ok {
			order = append(order, p.name)
		}
		groups[p.name] = append(groups[p.name], p)
	}
	for _, name := range order {
		ps := groups[name]
		fk := ForeignKey{Name: name, RefTable: ps[0].refTable}
		for _, p := range ps {
			fk.RefColumns = append(fk.RefColumns, p.refCol)
		}
		if len(ps) > 0 {
			fk.Column = ps[0].col
		}
		t.ForeignKeys = append(t.ForeignKeys, fk)
	}

	// 4. 索引：information_schema.statistics（按 index_name 分组）
	idxRows, err := q.QueryContext(ctx, `
SELECT index_name, non_unique, column_name
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY index_name, seq_in_index`, table)
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	defer idxRows.Close()
	type idxAcc struct {
		unique bool
		cols   []string
	}
	acc := map[string]*idxAcc{}
	var idxOrder []string
	for idxRows.Next() {
		var name, col string
		var nonUnique int
		if err := idxRows.Scan(&name, &nonUnique, &col); err != nil {
			return nil, err
		}
		a, ok := acc[name]
		if !ok {
			a = &idxAcc{unique: nonUnique == 0}
			acc[name] = a
			idxOrder = append(idxOrder, name)
		}
		a.cols = append(a.cols, col)
	}
	for _, name := range idxOrder {
		// 跳过主键索引（PRIMARY，已在 PrimaryKey 里）
		if strings.EqualFold(name, "PRIMARY") {
			continue
		}
		t.Indexes = append(t.Indexes, Index{
			Name:    name,
			Columns: acc[name].cols,
			Unique:  acc[name].unique,
		})
	}

	return t, nil
}
