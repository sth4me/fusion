package col

import (
	"encoding/json"
	"testing"
)

// TestScanJSON_ByteToMap []byte → map[string]any（成功 + 非法 JSON 失败）
func TestScanJSON_ByteToMap(t *testing.T) {
	// 成功
	var c Col[map[string]any]
	if err := c.Scan([]byte(`{"a":1,"b":"x"}`)); err != nil {
		t.Fatalf("scan []byte json into map: %v", err)
	}
	m := c.Get()
	if m["a"] != float64(1) || m["b"] != "x" {
		t.Errorf("got %v, want a=1 b=x", m)
	}

	// 非法 JSON → error（不静默置零）
	var c2 Col[map[string]any]
	err := c2.Scan([]byte(`{invalid}`))
	if err == nil {
		t.Fatal("scan invalid json into map should error")
	}
}

// TestScanJSON_ByteToSliceAny []byte → []any
func TestScanJSON_ByteToSliceAny(t *testing.T) {
	var c Col[[]any]
	if err := c.Scan([]byte(`[1,2,"x"]`)); err != nil {
		t.Fatalf("scan []byte json into []any: %v", err)
	}
	s := c.Get()
	if len(s) != 3 || s[0] != float64(1) || s[2] != "x" {
		t.Errorf("got %v", s)
	}
}

// TestScanJSON_ByteToSliceInt []byte → []int
func TestScanJSON_ByteToSliceInt(t *testing.T) {
	var c Col[[]int]
	if err := c.Scan([]byte(`[1,2,3]`)); err != nil {
		t.Fatalf("scan []byte json into []int: %v", err)
	}
	s := c.Get()
	if len(s) != 3 || s[0] != 1 || s[2] != 3 {
		t.Errorf("got %v", s)
	}
}

// TestScanJSON_ByteToStruct []byte → struct（匹配成功 + 不匹配失败）
func TestScanJSON_ByteToStruct(t *testing.T) {
	type point struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	// 成功
	var c Col[point]
	if err := c.Scan([]byte(`{"x":1,"y":2}`)); err != nil {
		t.Fatalf("scan []byte json into struct: %v", err)
	}
	p := c.Get()
	if p.X != 1 || p.Y != 2 {
		t.Errorf("got %+v, want {1 2}", p)
	}

	// 字段类型不匹配 → error（如 x 传字符串）
	var c2 Col[point]
	if err := c2.Scan([]byte(`{"x":"notint","y":2}`)); err == nil {
		t.Fatal("scan type-mismatched json into struct should error")
	}
}

// TestScanJSON_StringToMap string 源也要覆盖
func TestScanJSON_StringToMap(t *testing.T) {
	var c Col[map[string]any]
	if err := c.Scan(`{"k":"v"}`); err != nil {
		t.Fatalf("scan string json into map: %v", err)
	}
	if c.Get()["k"] != "v" {
		t.Errorf("got %v", c.Get())
	}
}

// TestScanJSON_EmptyByte 空 []byte → map 失败
func TestScanJSON_EmptyByte(t *testing.T) {
	var c Col[map[string]any]
	if err := c.Scan([]byte("")); err == nil {
		t.Fatal("scan empty []byte into map should error")
	}
}

// TestScanJSON_RegressionNumeric 回归：[]byte("123") → int64 仍走 numeric fallback
func TestScanJSON_RegressionNumeric(t *testing.T) {
	var c Col[int64]
	if err := c.Scan([]byte("123")); err != nil {
		t.Fatalf("scan []byte 123 into int64 (numeric path): %v", err)
	}
	if c.Get() != 123 {
		t.Errorf("got %v, want 123", c.Get())
	}
}

// TestScanJSON_RegressionBool 回归：[]byte("true") → bool 不被 json 抢（走原 bool 路径）
func TestScanJSON_RegressionBool(t *testing.T) {
	var c Col[bool]
	// bool 目标不应走 json 分支；scanBoolFromIntish 处理 "true"
	if err := c.Scan([]byte("true")); err != nil {
		t.Fatalf("scan []byte true into bool: %v", err)
	}
	if !c.Get() {
		t.Error("got false, want true")
	}
}

// TestScanJSON_Unmarshaler 实现了 json.Unmarshaler 的自定义类型
func TestScanJSON_Unmarshaler(t *testing.T) {
	// customJSON 实现了 json.Unmarshaler
	var c Col[customJSON]
	if err := c.Scan([]byte(`{"parsed":true}`)); err != nil {
		t.Fatalf("scan into json.Unmarshaler: %v", err)
	}
	if !c.Get().parsed {
		t.Error("customJSON.parsed should be true")
	}
}

// customJSON 是测试用的 json.Unmarshaler 类型。
type customJSON struct {
	parsed bool
}

func (c *customJSON) UnmarshalJSON(data []byte) error {
	var m map[string]bool
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.parsed = m["parsed"]
	return nil
}
