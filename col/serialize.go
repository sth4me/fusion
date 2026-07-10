package col

import (
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"time"
)

// maybeDeref 若 v 是指针则解引用返回底层值；nil 指针返回 nil（表示 SQL NULL）。
func maybeDeref(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	return rv.Interface()
}

// Value 实现 driver.Valuer，供 database/sql 扫描参数时使用。
// 对指针类型（Col[*string]），nil 表示 NULL。
func (c Col[T]) Value() (driver.Value, error) {
	return driverVal(derefAny(c.val)), nil
}

func driverVal(v any) driver.Value {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case driver.Valuer:
		val, err := x.Value()
		if err != nil {
			return nil
		}
		return val
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	}
	// fallback：map/slice/struct → JSON 字节（写入 PG jsonb 等 JSON 列）。
	// 与 scan_helper.go 的 unmarshalJSON 对称——读用 json.Unmarshal，写用 json.Marshal。
	// 让 Col[map[string]any]/Col[[]int]/Col[struct] 无需 col.Json 包装即可双向 JSON。
	if isJSONKind(v) {
		b, err := json.Marshal(v)
		if err != nil {
			// Marshal 失败返回 nil（driver 会当 NULL；极少见，如循环引用）
			return nil
		}
		return b
	}
	return v
}

// isJSONKind 报告 v 是否为适合 JSON 序列化的 Go 类型（map/slice/struct）。
// 排除 time.Time（已有 Valuer 处理）和已实现 driver.Valuer 的类型（上面已处理）。
func isJSONKind(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Struct:
		// time.Time 是 struct 但已有专门的 Valuer，排除
		if rv.Type() == reflect.TypeOf(time.Time{}) {
			return false
		}
		return true
	}
	return false
}

// Scan 实现 sql.Scanner，供 database/sql 把查询结果扫描进 Col。
func (c *Col[T]) Scan(src any) error {
	if src == nil {
		// NULL → 零值（若 T 是指针则为 nil）
		c.val = setPtrNil(c.val)
		return nil
	}
	rv := reflect.ValueOf(&c.val).Elem()
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		elem := rv.Elem()
		if err := assignReflect(elem, src); err != nil {
			return err
		}
		return nil
	}
	// 非指针：直接赋值
	if err := assignReflect(rv, src); err != nil {
		return err
	}
	return nil
}

// MarshalJSON 实现 json.Marshaler，序列化为内部值的 JSON（透明）。
func (c Col[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(maybeDeref(c.val))
}

// UnmarshalJSON 实现 json.Unmarshaler，从 JSON 反序列化到内部值。
func (c *Col[T]) UnmarshalJSON(data []byte) error {
	// 若 T 是指针，需要分配后填充
	rv := reflect.ValueOf(&c.val).Elem()
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		return json.Unmarshal(data, rv.Interface())
	}
	return json.Unmarshal(data, &c.val)
}
