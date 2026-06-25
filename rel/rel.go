// Package rel 实现关联字段描述符 Rel[T] / RelMany[T]。
//
// 关联字段携带 loaded 状态，根治"未 Preload 就访问"的空指针陷阱：
//   - nil/未加载：Loaded()=false，Get()/All() 返回 ErrNotLoaded（明确错误，非 panic）
//   - 加载后无关联：IsNil()=true（区分"未加载"与"加载了但无数据"）
//
// Rel/RelMany 实现 _isRelation() 空方法，被 meta 包通过反射识别为关联字段
// （CollectFields 跳过，不参与 DML 列映射）。
//
// 详见 docs/DESIGN.md 决策 1、6。
package rel

import (
	"encoding/json"
	"errors"
)

// ErrNotLoaded 表示关联字段未被 Preload 加载。
// 访问未加载的关联时返回，避免隐蔽的空指针或空壳数据。
var ErrNotLoaded = errors.New("fusion: relation not loaded (did you forget Preload?)")

// Rel 是单值关联描述符（belongs_to / has_one）。
// val 为 nil 表示未加载或无关联；用 Loaded() 区分二者。
type Rel[T any] struct {
	val    *T
	loaded bool
}

// _isRelation 标记 Rel 为关联字段，供 meta 反射识别（meta.relationMarker）。
func (Rel[T]) _isRelation() {}

// Loaded 报告关联是否被 Preload 加载过。
func (r *Rel[T]) Loaded() bool { return r.loaded }

// IsNil 报告加载后是否无关联（nil）。
// 未加载时也返回 true；用 Loaded() 先判断。
func (r *Rel[T]) IsNil() bool { return r.val == nil }

// Get 返回关联值。未加载返回 ErrNotLoaded；加载但无关联返回 nil, nil。
func (r *Rel[T]) Get() (*T, error) {
	if !r.loaded {
		return nil, ErrNotLoaded
	}
	return r.val, nil
}

// MustGet 返回关联值，未加载时 panic（用于 debug 快速定位遗漏 Preload）。
func (r *Rel[T]) MustGet() *T {
	if !r.loaded {
		panic(ErrNotLoaded)
	}
	return r.val
}

// setLoad 由 Preload 内部回填用（不导出给用户）。
func (r *Rel[T]) setLoad(v *T) {
	r.val = v
	r.loaded = true
}

// SetLoad 是 setLoad 的导出包装，供 relation 包通过反射/接口回填。
// （reflect 无法调用未导出方法，故提供导出入口。）
func (r *Rel[T]) SetLoad(v *T) { r.setLoad(v) }

// MarshalJSON 实现 JSON 透明序列化：未加载或 nil → null；有值 → 序列化值。
func (r Rel[T]) MarshalJSON() ([]byte, error) {
	if r.val == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*r.val)
}

// UnmarshalJSON 实现 JSON 透明反序列化。
func (r *Rel[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		r.val = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	r.val = &v
	return nil
}

// RelMany 是集合关联描述符（has_many / many_to_many）。
// val 为 nil 表示未加载；len==0 表示加载了但为空。
type RelMany[T any] struct {
	val    []T
	loaded bool
}

// _isRelation 标记 RelMany 为关联字段。
func (RelMany[T]) _isRelation() {}

// Loaded 报告关联是否被 Preload 加载过。
func (r *RelMany[T]) Loaded() bool { return r.loaded }

// All 返回关联切片。未加载返回 ErrNotLoaded。
func (r *RelMany[T]) All() ([]T, error) {
	if !r.loaded {
		return nil, ErrNotLoaded
	}
	return r.val, nil
}

// MustAll 返回关联切片，未加载时 panic。
func (r *RelMany[T]) MustAll() []T {
	if !r.loaded {
		panic(ErrNotLoaded)
	}
	return r.val
}

// Len 返回关联数量。未加载返回 -1。
func (r *RelMany[T]) Len() int {
	if !r.loaded {
		return -1
	}
	return len(r.val)
}

// setLoad 由 Preload 内部回填用。
func (r *RelMany[T]) setLoad(v []T) {
	r.val = v
	r.loaded = true
}

// SetLoad 是 setLoad 的导出包装，供 relation 包回填。
func (r *RelMany[T]) SetLoad(v []T) { r.setLoad(v) }

// MarshalJSON 实现 JSON 透明序列化：未加载 → null；有值 → 序列化切片。
func (r RelMany[T]) MarshalJSON() ([]byte, error) {
	if !r.loaded {
		return []byte("null"), nil
	}
	return json.Marshal(r.val)
}

// UnmarshalJSON 实现 JSON 透明反序列化。
func (r *RelMany[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		r.val = nil
		return nil
	}
	var v []T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	r.val = v
	r.loaded = true
	return nil
}
