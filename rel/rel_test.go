package rel

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRelNotLoaded(t *testing.T) {
	var r Rel[int]
	if r.Loaded() {
		t.Error("zero Rel should not be loaded")
	}
	v, err := r.Get()
	if !errors.Is(err, ErrNotLoaded) {
		t.Errorf("Get on unloaded should return ErrNotLoaded, got %v", err)
	}
	if v != nil {
		t.Errorf("value should be nil, got %v", v)
	}
}

func TestRelMustGetPanics(t *testing.T) {
	var r Rel[int]
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustGet on unloaded should panic")
		}
	}()
	_ = r.MustGet()
}

func TestRelSetLoad(t *testing.T) {
	var r Rel[string]
	v := "hello"
	r.setLoad(&v)
	if !r.Loaded() {
		t.Error("should be loaded after setLoad")
	}
	if r.IsNil() {
		t.Error("should not be nil")
	}
	got, err := r.Get()
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if *got != "hello" {
		t.Errorf("got %q, want hello", *got)
	}
}

func TestRelLoadedButNil(t *testing.T) {
	// 加载了但无关联（如 belongs_to 但外键为空）
	var r Rel[int]
	r.setLoad(nil)
	if !r.Loaded() {
		t.Error("should be loaded")
	}
	if !r.IsNil() {
		t.Error("should be nil (loaded but no relation)")
	}
	got, err := r.Get()
	if err != nil {
		t.Errorf("Get err: %v", err)
	}
	if got != nil {
		t.Errorf("value should be nil")
	}
}

func TestRelManyNotLoaded(t *testing.T) {
	var r RelMany[int]
	if r.Loaded() {
		t.Error("zero RelMany should not be loaded")
	}
	if r.Len() != -1 {
		t.Errorf("Len on unloaded got %d, want -1", r.Len())
	}
	_, err := r.All()
	if !errors.Is(err, ErrNotLoaded) {
		t.Errorf("All on unloaded should return ErrNotLoaded, got %v", err)
	}
}

func TestRelManySetLoad(t *testing.T) {
	var r RelMany[int]
	r.setLoad([]int{1, 2, 3})
	if !r.Loaded() {
		t.Error("should be loaded")
	}
	if r.Len() != 3 {
		t.Errorf("Len got %d, want 3", r.Len())
	}
	all, err := r.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0] != 1 {
		t.Errorf("All got %v", all)
	}
}

func TestRelManySetLoadEmpty(t *testing.T) {
	// 加载了但为空（区分未加载 vs 加载空）
	var r RelMany[int]
	r.setLoad([]int{})
	if !r.Loaded() {
		t.Error("should be loaded")
	}
	if r.Len() != 0 {
		t.Errorf("Len got %d, want 0", r.Len())
	}
}

// JSON 透明：未加载 → null；有值 → 序列化值
func TestRelJSONNotLoaded(t *testing.T) {
	var r Rel[string]
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "null" {
		t.Errorf("unloaded Rel json got %s, want null", b)
	}
}

func TestRelJSONWithValue(t *testing.T) {
	type Inner struct{ X int }
	var r Rel[Inner]
	v := Inner{X: 42}
	r.setLoad(&v)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"X":42}` {
		t.Errorf("loaded Rel json got %s, want {\"X\":42}", b)
	}
}

func TestRelManyJSONNotLoaded(t *testing.T) {
	var r RelMany[int]
	b, _ := json.Marshal(r)
	if string(b) != "null" {
		t.Errorf("unloaded RelMany json got %s, want null", b)
	}
}

func TestRelManyJSONWithValue(t *testing.T) {
	var r RelMany[int]
	r.setLoad([]int{1, 2})
	b, _ := json.Marshal(r)
	if string(b) != "[1,2]" {
		t.Errorf("loaded RelMany json got %s, want [1,2]", b)
	}
}

// 验证 relationMarker 识别：Rel/RelMany 实现 _isRelation()，反射能命中
func TestRelationMarker(t *testing.T) {
	// 模拟 meta.isRelationType 的探测逻辑
	markerType := reflect.TypeOf((*interface{ _isRelation() })(nil)).Elem()
	relT := reflect.TypeOf(Rel[int]{})
	relManyT := reflect.TypeOf(RelMany[int]{})
	if !relT.Implements(markerType) {
		t.Error("Rel[T] should implement _isRelation marker")
	}
	if !relManyT.Implements(markerType) {
		t.Error("RelMany[T] should implement _isRelation marker")
	}
}

// 验证 Rel 的指针方法也在（setLoad 是指针接收者）
func TestRelPointerMethods(t *testing.T) {
	// 指针类型也实现 marker
	markerType := reflect.TypeOf((*interface{ _isRelation() })(nil)).Elem()
	relPtrT := reflect.TypeOf((*Rel[int])(nil))
	if !relPtrT.Implements(markerType) {
		t.Error("*Rel[T] should implement marker")
	}
}
