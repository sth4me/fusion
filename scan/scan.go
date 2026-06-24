// Package scan 把 *sql.Rows 扫描进模型实例。
//
// 全包装方案下，模型字段是 col.Col[T]，它们实现 sql.Scanner（见 col 包）。
// scan 包按结果集列名路由到对应字段，构造 []any 调用 rows.Scan。
//
// 详见 docs/DESIGN.md 决策 1。
package scan

import (
	"database/sql"
	"fmt"
	"reflect"

	"fusion/meta"
)

// scanner 是可被 rows.Scan 接受的目标接口（即 sql.Scanner）。
type scanner interface {
	Scan(src any) error
}

// Rows 抽象 *sql.Rows 所需的最小接口，便于测试与扩展（raw 查询复用）。
type Rows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// All 把 rows 扫描进 *[]T，逐行扫描直至 Next 返回 false。
// model 必须是已注册的模型类型（字段为 col.Col[T]）。
//
// 路由：rows 列名 → ModelMeta.byCol → 结构体字段 → *Col[T]（Scanner）。
func All[T any](rows Rows, m *meta.ModelMeta) ([]T, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("fusion: read columns: %w", err)
	}

	// 为每个结果集列找到对应的字段索引（结构体内序号）。
	fieldIdx := make([]int, len(cols))
	for i, c := range cols {
		fm := m.FieldByColumn(c)
		if fm == nil {
			// 列名找不到对应字段：标记 -1，扫描时用丢弃占位符。
			fieldIdx[i] = -1
			continue
		}
		// 在结构体里找到该字段名对应的索引。
		fieldIdx[i] = fieldIndexByName(m, fm.FieldName)
	}

	out := make([]T, 0, 8)
	for rows.Next() {
		var row T
		rv := reflect.ValueOf(&row).Elem()

		dest := make([]any, len(cols))
		for i, idx := range fieldIdx {
			if idx < 0 {
				// 无对应字段：丢弃该列
				var discard any
				dest[i] = &discard
				continue
			}
			fv := rv.Field(idx).Addr().Interface()
			if sc, ok := fv.(scanner); ok {
				dest[i] = sc
			} else {
				// 非 Col 字段（理论上全包装下不会出现），直接用字段地址
				dest[i] = fv
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return out, fmt.Errorf("fusion: scan row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("fusion: iterate rows: %w", err)
	}
	return out, nil
}

// fieldIndexByName 返回结构体字段名对应的索引，找不到返回 -1。
func fieldIndexByName(m *meta.ModelMeta, name string) int {
	for i := 0; i < m.Type.NumField(); i++ {
		if m.Type.Field(i).Name == name {
			return i
		}
	}
	return -1
}

// One 扫描单行（调用方已确认 Next 返回 true）。用于 LIMIT 1 / Get 场景。
func One[T any](rows *sql.Rows, m *meta.ModelMeta) (T, error) {
	var zero T
	// 复用 All 的列路由逻辑，但只扫一行。
	cols, err := rows.Columns()
	if err != nil {
		return zero, err
	}
	fieldIdx := make([]int, len(cols))
	for i, c := range cols {
		fm := m.FieldByColumn(c)
		if fm == nil {
			fieldIdx[i] = -1
			continue
		}
		fieldIdx[i] = fieldIndexByName(m, fm.FieldName)
	}
	if !rows.Next() {
		return zero, sql.ErrNoRows
	}
	var row T
	rv := reflect.ValueOf(&row).Elem()
	dest := make([]any, len(cols))
	for i, idx := range fieldIdx {
		if idx < 0 {
			var discard any
			dest[i] = &discard
			continue
		}
		fv := rv.Field(idx).Addr().Interface()
		dest[i] = fv
	}
	if err := rows.Scan(dest...); err != nil {
		return zero, err
	}
	return row, nil
}
