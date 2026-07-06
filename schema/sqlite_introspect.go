package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// sqliteIntrospector 用 PRAGMA 系列指令内省 SQLite schema。
type sqliteIntrospector struct{}

func (sqliteIntrospector) ListTables(ctx context.Context, q Queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
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

// DescribeTable 用 PRAGMA table_info / foreign_key_list / index_list+index_info 内省。
func (sqliteIntrospector) DescribeTable(ctx context.Context, q Queryer, table string) (*Table, error) {
	t := &Table{Name: table}

	// 1. 列：PRAGMA table_info(<t>)
	//   cid | name | type | notnull | dflt_value | pk
	//   pk>0 表示主键列；复合主键时多行 pk 为 1,2,...
	colRows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return nil, fmt.Errorf("table_info: %w", err)
	}
	type colRow struct {
		cid      int
		name     string
		typ      string
		notNull  int
		dflt     sql.NullString
		pk       int
	}
	var colRowsData []colRow
	for colRows.Next() {
		var r colRow
		if err := colRows.Scan(&r.cid, &r.name, &r.typ, &r.notNull, &r.dflt, &r.pk); err != nil {
			colRows.Close()
			return nil, err
		}
		colRowsData = append(colRowsData, r)
	}
	colRows.Close()

	var pkOrder = map[int]string{} // pk 序号 → 列名，用于按序还原复合主键
	for _, r := range colRowsData {
		c := Column{
			Name:     r.name,
			SQLType:  r.typ,
			Nullable: r.notNull == 0,
		}
		if r.dflt.Valid {
			d := r.dflt.String
			c.Default = &d
		}
		t.Columns = append(t.Columns, c)
		if r.pk > 0 {
			pkOrder[r.pk] = r.name
		}
	}
	// 按序号还原主键列（pk=1,2,...）
	for i := 1; i <= len(colRowsData); i++ {
		if name, ok := pkOrder[i]; ok {
			t.PrimaryKey = append(t.PrimaryKey, name)
		} else {
			break
		}
	}

	// 2. 外键：PRAGMA foreign_key_list(<t>)
	//   id | seq | table | from | to | on_update | on_delete | match
	fkRows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
	if err != nil {
		return nil, fmt.Errorf("foreign_key_list: %w", err)
	}
	// 同一约束（同 id）多行 = 复合外键，按 seq 排序对齐 from/to。
	type fkRow struct {
		id    int
		seq   int
		table string
		from  string
		to    string
	}
	var fkRowsData []fkRow
	for fkRows.Next() {
		var r fkRow
		var onUpdate, onDelete, match string
		if err := fkRows.Scan(&r.id, &r.seq, &r.table, &r.from, &r.to, &onUpdate, &onDelete, &match); err != nil {
			fkRows.Close()
			return nil, err
		}
		fkRowsData = append(fkRowsData, r)
	}
	fkRows.Close()
	// 按 id 分组，组内按 seq 排序
	byID := map[int][]fkRow{}
	for _, r := range fkRowsData {
		byID[r.id] = append(byID[r.id], r)
	}
	for id, rows := range byID {
		// rows 已按 seq 升序（PRAGMA 保证）；构造 ForeignKey
		fk := ForeignKey{
			Name:     fmt.Sprintf("fk_%s_%d", table, id),
			RefTable: rows[0].table,
		}
		for _, r := range rows {
			fk.Column += r.from        // 复合外键用拼接（当前单列场景仅一个）
			fk.RefColumns = append(fk.RefColumns, r.to)
		}
		// 单列场景修正 Column（取首个 from）
		if len(rows) == 1 {
			fk.Column = rows[0].from
		}
		t.ForeignKeys = append(t.ForeignKeys, fk)
	}

	// 3. 索引：PRAGMA index_list(<t>) + PRAGMA index_info(<idx>)
	idxListRows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	if err != nil {
		return nil, fmt.Errorf("index_list: %w", err)
	}
	type idxListRow struct {
		seq     int
		name    string
		unique  int
		origin  string
		partial int
	}
	var idxList []idxListRow
	for idxListRows.Next() {
		var r idxListRow
		if err := idxListRows.Scan(&r.seq, &r.name, &r.unique, &r.origin, &r.partial); err != nil {
			idxListRows.Close()
			return nil, err
		}
		idxList = append(idxList, r)
	}
	idxListRows.Close()
	for _, il := range idxList {
		// origin: "c"=CREATE INDEX, "u"=UNIQUE 约束, "pk"=主键
		// 主键索引跳过（已在 PrimaryKey 里）
		if il.origin == "pk" {
			continue
		}
		infoRows, err := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_info(%q)`, il.name))
		if err != nil {
			return nil, fmt.Errorf("index_info %s: %w", il.name, err)
		}
		var cols []string
		for infoRows.Next() {
			var seqno int
			var cid int
			var name string
			if err := infoRows.Scan(&seqno, &cid, &name); err != nil {
				infoRows.Close()
				return nil, err
			}
			cols = append(cols, name)
		}
		infoRows.Close()
		t.Indexes = append(t.Indexes, Index{
			Name:    il.name,
			Columns: cols,
			Unique:  il.unique == 1 || il.origin == "u",
		})
	}

	return t, nil
}
