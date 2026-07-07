package col

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"fusion/meta"
)

// Json 是 JSON 字段包装类型，用于 PostgreSQL jsonb / MySQL JSON / SQLite TEXT(json)。
// 用法：声明模型字段为 orm.Json[YourStruct] 或 orm.Json[[]string]，框架自动
// 在写入时 json.Marshal、读取时 json.Unmarshal。
//
// 零侵入：Json 实现 driver.Valuer + sql.Scanner + FieldDescriptor，复用 Col[T] 的序列化路径。
type Json[T any] struct {
	V    T
	set  bool
	col  string // 列名（由 meta.Register 经 SetMeta 填充）
}

// Set 设置值。
func (j *Json[T]) Set(v T) { j.V = v; j.set = true }

// Get 返回值。
func (j *Json[T]) Get() T { return j.V }

// IsSet 报告是否被显式赋值。
func (j *Json[T]) IsSet() bool { return j.set }

// SQLValue 返回 SQL 值（JSON 字节），供 DML 参数使用。
func (j *Json[T]) SQLValue() (any, error) { return j.Value() }

// ColName 返回列名（由 meta.Register 填充）。
func (j *Json[T]) ColName() string { return j.col }

// SetMeta 由 meta.Register 反射调用，填充列名（实现 FieldDescriptor）。
func (j *Json[T]) SetMeta(m meta.FieldMeta) { j.col = m.Column }

// Value 实现 driver.Valuer，把 V 序列化为 JSON。
func (j Json[T]) Value() (driver.Value, error) {
	b, err := json.Marshal(j.V)
	if err != nil {
		return nil, fmt.Errorf("fusion: json marshal: %w", err)
	}
	return b, nil
}

// Scan 实现 sql.Scanner，把数据库返回的 JSON 反序列化进 V。
func (j *Json[T]) Scan(src any) error {
	if src == nil {
		var zero T
		j.V = zero
		return nil
	}
	var b []byte
	switch s := src.(type) {
	case []byte:
		b = s
	case string:
		b = []byte(s)
	default:
		return fmt.Errorf("fusion: json scan: unsupported source type %T", src)
	}
	if err := json.Unmarshal(b, &j.V); err != nil {
		return fmt.Errorf("fusion: json unmarshal: %w", err)
	}
	return nil
}

// MarshalJSON 透明 JSON 序列化（直接序列化 V）。
func (j Json[T]) MarshalJSON() ([]byte, error) { return json.Marshal(j.V) }

// UnmarshalJSON 透明 JSON 反序列化。
func (j *Json[T]) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &j.V); err != nil {
		return err
	}
	j.set = true
	return nil
}
