// Package hook 提供 Before/After 回调机制。
//
// 钩子按「事件类型 × 模型类型」注册，回调签名为 func(ctx, target) error。
// target 是实体指针（any），用户在回调内类型断言为具体模型。
// Before 钩子返回 error 会中止操作；After 钩子的 error 向上传递。
//
// 详见 docs/DESIGN.md 钩子部分。
package hook

import (
	"context"
	"reflect"
	"sync"
)

// Event 是钩子事件类型。
type Event int

const (
	BeforeCreate Event = iota
	AfterCreate
	BeforeUpdate
	AfterUpdate
	BeforeDelete
	AfterDelete
)

// String 返回事件名，用于日志。
func (e Event) String() string {
	switch e {
	case BeforeCreate:
		return "BeforeCreate"
	case AfterCreate:
		return "AfterCreate"
	case BeforeUpdate:
		return "BeforeUpdate"
	case AfterUpdate:
		return "AfterUpdate"
	case BeforeDelete:
		return "BeforeDelete"
	case AfterDelete:
		return "AfterDelete"
	}
	return "Unknown"
}

// Func 是钩子回调。ctx 透传上下文，target 是实体指针（any）。
// 返回 error 会中止当前操作（Before）或向上传递（After）。
type Func func(ctx context.Context, target any) error

// key 是 (事件 × 模型类型) 的组合。
type key struct {
	event Event
	rt    reflect.Type
}

var (
	mu       sync.RWMutex
	registry = make(map[key][]Func)
)

// Register 为模型类型 modelPtr（应为指向模型的指针，如 (*User)(nil)）
// 在指定事件上注册钩子。返回一个注销函数。
func Register(modelPtr any, event Event, fn Func) (unregister func()) {
	rt := derefType(modelPtr)
	k := key{event: event, rt: rt}
	mu.Lock()
	registry[k] = append(registry[k], fn)
	idx := len(registry[k]) - 1
	mu.Unlock()

	return func() {
		mu.Lock()
		defer mu.Unlock()
		if fns, ok := registry[k]; ok {
			// 用 nil 标记移除（保持索引稳定，避免并发问题）
			fns[idx] = nil
			// 压缩全 nil 的条目
			allNil := true
			for _, f := range fns {
				if f != nil {
					allNil = false
					break
				}
			}
			if allNil {
				delete(registry, k)
			} else {
				registry[k] = fns
			}
		}
	}
}

// Trigger 触发指定模型类型 + 事件的全部钩子，按注册顺序执行。
// 任一钩子返回 error 即停止并返回该错误。target 透传给回调（可为 nil）。
func Trigger(ctx context.Context, modelPtr any, event Event) error {
	return TriggerByType(ctx, derefType(modelPtr), event, modelPtr)
}

// TriggerByType 按 reflect.Type 触发钩子（用于无具体实例的场景，如 Delete）。
// target 透传给回调；modelRT 为模型元素类型。
func TriggerByType(ctx context.Context, modelRT reflect.Type, event Event, target any) error {
	k := key{event: event, rt: modelRT}
	mu.RLock()
	fns := registry[k]
	cb := make([]Func, len(fns))
	copy(cb, fns)
	mu.RUnlock()

	for _, fn := range cb {
		if fn == nil {
			continue
		}
		if err := fn(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

// derefType 取 modelPtr 的 reflect.Type；若是指针取其元素类型。
// （模型注册时按元素类型匹配，target 传指针或值都能触发）
func derefType(v any) reflect.Type {
	rt := reflect.TypeOf(v)
	if rt == nil {
		return nil
	}
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	return rt
}
