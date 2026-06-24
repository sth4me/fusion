package col

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"time"
)

// setPtrNil 若 T 是指针则返回 nil 指针，否则返回零值。
func setPtrNil[T any](zero T) T {
	rv := reflect.ValueOf(&zero).Elem()
	if rv.Kind() == reflect.Ptr {
		rv.Set(reflect.Zero(rv.Type()))
	}
	return zero
}

// assignReflect 把 SQL 源值 src 赋给 rv（可设值的反射 Value）。

// assignReflect 把 SQL 源值 src 赋给 rv（可设值的反射 Value）。
func assignReflect(rv reflect.Value, src any) error {
	// 时间类型特殊处理（SQL 常返回 time.Time 或字符串）
	if rv.Type() == reflect.TypeOf(time.Time{}) {
		return assignTime(rv, src)
	}
	// 接收方实现了 Scanner，优先使用
	if rv.CanAddr() {
		if sc, ok := rv.Addr().Interface().(sqlScanner); ok {
			return sc.Scan(src)
		}
	}

	srcVal := reflect.ValueOf(src)
	// 直接类型匹配
	if srcVal.Type().AssignableTo(rv.Type()) {
		rv.Set(srcVal)
		return nil
	}
	// 尝试转换（数值类型兼容）
	if srcVal.Type().ConvertibleTo(rv.Type()) {
		rv.Set(srcVal.Convert(rv.Type()))
		return nil
	}
	return fmt.Errorf("fusion: cannot scan %T into %s", src, rv.Type())
}

// assignTime 把 src（可能是 time.Time、string、[]byte）赋给 time.Time 字段。
func assignTime(rv reflect.Value, src any) error {
	switch x := src.(type) {
	case time.Time:
		rv.Set(reflect.ValueOf(x))
		return nil
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			rv.Set(reflect.ValueOf(t))
			return nil
		}
		// 回退到其他常见格式
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, x); err == nil {
				rv.Set(reflect.ValueOf(t))
				return nil
			}
		}
		rv.Set(reflect.ValueOf(time.Time{}))
		return nil
	case []byte:
		return assignTime(rv, string(x))
	}
	return fmt.Errorf("fusion: cannot scan %T into time.Time", src)
}

// sqlScanner 是 database/sql.Scanner 的本地别名，避免直接导入 database/sql
// 带来循环依赖风险（col 不应依赖上层）。
type sqlScanner interface {
	Scan(src any) error
}

// 确保 Col 实现关键接口
var (
	_ driver.Valuer  = Col[int]{}
	_ sqlScanner     = (*Col[int])(nil)
	_ jsonMarshaler  = Col[int]{}
	_ jsonUnmarshaler = (*Col[int])(nil)
)

type jsonMarshaler interface {
	MarshalJSON() ([]byte, error)
}
type jsonUnmarshaler interface {
	UnmarshalJSON([]byte) error
}
