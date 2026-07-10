package col

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
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

	// bool 特殊处理：SQLite/MySQL 把 BOOLEAN 存为 INTEGER(0/1)，
	// 驱动返回 int64/int/[]byte，不能直接 Convert 到 bool。按"非零=true"转换。
	if rv.Kind() == reflect.Bool {
		if b, ok := scanBoolFromIntish(src); ok {
			rv.SetBool(b)
			return nil
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
	// fallback：string / []byte → 数值类型（PG numeric/money 等列驱动默认返回 string）。
	// 解析失败时不静默置零，落到下面的 return error（与 assignTime 失败语义一致）。
	if s, ok := srcString(src); ok {
		if v, ok := parseNumeric(s, rv); ok {
			rv.Set(v)
			return nil
		}
	}
	// fallback：[]byte / string（jsonb 原始 JSON）→ map/slice/struct/json.Unmarshaler。
	// PG jsonb 经 pgx 返回 []byte，目标若是结构化类型则 json.Unmarshal。
	// 仅对 map/slice/struct/json.Unmarshaler 目标尝试，避免与 numeric/bool/time 冲突。
	// 解析失败不吞错误，落到 return error。
	if data, ok := toBytes(src); ok {
		if ok := unmarshalJSON(data, rv); ok {
			return nil
		}
	}
	return fmt.Errorf("fusion: cannot scan %T into %s", src, rv.Type())
}

// srcString 把 string / []byte 源值统一成 string；其它类型返回 false。
func srcString(src any) (string, bool) {
	switch x := src.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

// parseNumeric 按 rv.Kind() 用 strconv 解析数值字符串。
// 整数目标遇到带小数的字符串会失败（ParseInt 不接受小数点）——这是有意的，不截断。
// 非数值 Kind 返回 false（交回原错误路径）。
func parseNumeric(s string, rv reflect.Value) (reflect.Value, bool) {
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, rv.Type().Bits())
		if err != nil {
			return reflect.Value{}, false
		}
		return reflect.ValueOf(n).Convert(rv.Type()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, rv.Type().Bits())
		if err != nil {
			return reflect.Value{}, false
		}
		return reflect.ValueOf(n).Convert(rv.Type()), true
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, rv.Type().Bits())
		if err != nil {
			return reflect.Value{}, false
		}
		return reflect.ValueOf(n).Convert(rv.Type()), true
	}
	return reflect.Value{}, false
}

// toBytes 把 []byte / string 源值统一为 []byte；其它类型返回 false。
func toBytes(src any) ([]byte, bool) {
	switch x := src.(type) {
	case []byte:
		return x, true
	case string:
		return []byte(x), true
	}
	return nil, false
}

// unmarshalJSON 尝试把 JSON 字节解析进 rv（可寻址）。
// 仅当目标 Kind 是 map/slice/struct，或目标实现了 json.Unmarshaler 时才尝试。
// 数值/bool/string/time 等目标不处理（交回原错误路径，避免与 numeric/bool 冲突）。
// 成功返回 true；失败或不适用返回 false（不报错，让上层走 return error）。
func unmarshalJSON(data []byte, rv reflect.Value) bool {
	// 空数据不处理（空 []byte 不是合法 JSON，交回原路径报错更清晰）
	if len(data) == 0 {
		return false
	}
	// 优先：目标实现了 json.Unmarshaler（自定义类型的自定义反序列化逻辑）
	if rv.CanAddr() {
		if u, ok := rv.Addr().Interface().(json.Unmarshaler); ok {
			if err := u.UnmarshalJSON(data); err != nil {
				return false // 解析失败交回原路径报错
			}
			return true
		}
	}
	// 仅对 map/slice/struct 目标尝试 json.Unmarshal
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Struct:
		// map/slice 需要 Init（零值 map/slice 无法直接 Unmarshal）
		if rv.Kind() == reflect.Map && rv.IsNil() {
			rv.Set(reflect.MakeMap(rv.Type()))
		}
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		}
		if err := json.Unmarshal(data, rv.Addr().Interface()); err != nil {
			return false
		}
		return true
	}
	return false
}
// 非 0 → true；0 → false。非整数类返回 (false, false)。
func scanBoolFromIntish(src any) (bool, bool) {
	switch x := src.(type) {
	case int64:
		return x != 0, true
	case int:
		return x != 0, true
	case int32:
		return x != 0, true
	case []byte:
		// "1"/"0"/"true"/"false" 等
		s := string(x)
		if s == "1" || s == "true" || s == "TRUE" || s == "t" || s == "T" {
			return true, true
		}
		if s == "0" || s == "false" || s == "FALSE" || s == "f" || s == "F" {
			return false, true
		}
		return false, false
	case string:
		if x == "1" || x == "true" || x == "TRUE" {
			return true, true
		}
		if x == "0" || x == "false" || x == "FALSE" {
			return false, true
		}
		return false, false
	}
	return false, false
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
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02 15:04:05.999999999 -0700 MST", // Go time.Time.String() 带小数秒
			"2006-01-02 15:04:05 -0700 MST",           // Go time.Time.String() 无小数秒
			"2006-01-02 15:04:05",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, x); err == nil {
				rv.Set(reflect.ValueOf(t))
				return nil
			}
		}
		// 解析全部失败：返回 error 而非静默置零（避免数据损坏无感知）。
		return fmt.Errorf("fusion: cannot parse time %q into time.Time (tried common layouts)", x)
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
