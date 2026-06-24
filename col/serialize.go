package col

import (
	"database/sql/driver"
	"encoding/json"
	"reflect"
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
	return v
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
