package col

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// TestScanNumeric_StringToFloat string → float64（成功 + 失败）
func TestScanNumeric_StringToFloat(t *testing.T) {
	// 成功
	var c Col[float64]
	if err := c.Scan("100.50"); err != nil {
		t.Fatalf("scan string 100.50 into float64: %v", err)
	}
	if c.Get() != 100.5 {
		t.Errorf("got %v, want 100.5", c.Get())
	}

	// 整数字符串转 float
	var c2 Col[float64]
	if err := c2.Scan("100"); err != nil {
		t.Fatalf("scan string 100 into float64: %v", err)
	}
	if c2.Get() != 100.0 {
		t.Errorf("got %v, want 100.0", c2.Get())
	}

	// 非法字符串 → error（不静默置零）
	var c3 Col[float64]
	err := c3.Scan("abc")
	if err == nil {
		t.Fatal("scan 'abc' into float64 should error")
	}
	if c3.Get() != 0 {
		t.Errorf("on parse failure, value should remain zero, got %v", c3.Get())
	}
}

// TestScanNumeric_StringToInt string → int64（整数成功 + 带小数失败 + 非法失败）
func TestScanNumeric_StringToInt(t *testing.T) {
	// 整数字符串成功
	var c Col[int64]
	if err := c.Scan("100"); err != nil {
		t.Fatalf("scan string 100 into int64: %v", err)
	}
	if c.Get() != 100 {
		t.Errorf("got %v, want 100", c.Get())
	}

	// 带小数 → error（不截断！规格第 5 行验收矩阵）
	var c2 Col[int64]
	err := c2.Scan("3.14")
	if err == nil {
		t.Fatal("scan '3.14' into int64 should error (no truncation)")
	}

	// 非法
	var c3 Col[int64]
	if err := c3.Scan("abc"); err == nil {
		t.Fatal("scan 'abc' into int64 should error")
	}
}

// TestScanNumeric_StringToUint string → uint64
func TestScanNumeric_StringToUint(t *testing.T) {
	var c Col[uint64]
	if err := c.Scan("42"); err != nil {
		t.Fatalf("scan string 42 into uint64: %v", err)
	}
	if c.Get() != 42 {
		t.Errorf("got %v, want 42", c.Get())
	}
}

// TestScanNumeric_StringToFloat32 string → float32
func TestScanNumeric_StringToFloat32(t *testing.T) {
	var c Col[float32]
	if err := c.Scan("3.14"); err != nil {
		t.Fatalf("scan string 3.14 into float32: %v", err)
	}
	// float32 精度，用 Approx 对比
	if c.Get() < 3.13 || c.Get() > 3.15 {
		t.Errorf("got %v, want ~3.14", c.Get())
	}
}

// TestScanNumeric_ByteSlice []byte → float64 / int64
func TestScanNumeric_ByteSlice(t *testing.T) {
	var f Col[float64]
	if err := f.Scan([]byte("3.14")); err != nil {
		t.Fatalf("scan []byte 3.14 into float64: %v", err)
	}
	if f.Get() != 3.14 {
		t.Errorf("got %v, want 3.14", f.Get())
	}

	var i Col[int64]
	if err := i.Scan([]byte("100")); err != nil {
		t.Fatalf("scan []byte 100 into int64: %v", err)
	}
	if i.Get() != 100 {
		t.Errorf("got %v, want 100", i.Get())
	}
}

// TestScanNumeric_EmptyString 空字符串 → int64 失败
func TestScanNumeric_EmptyString(t *testing.T) {
	var c Col[int64]
	if err := c.Scan(""); err == nil {
		t.Fatal("scan empty string into int64 should error")
	}
}

// TestScanNumeric_RegressionConvertibleTo 回归：int64 → float64 仍走 ConvertibleTo 成功
func TestScanNumeric_RegressionConvertibleTo(t *testing.T) {
	var c Col[float64]
	if err := c.Scan(int64(100)); err != nil {
		t.Fatalf("scan int64 100 into float64 (ConvertibleTo path): %v", err)
	}
	if c.Get() != 100.0 {
		t.Errorf("got %v, want 100.0", c.Get())
	}
}

// TestScanNumeric_RegressionUUID 回归：[16]byte → uuid.UUID 仍成功
func TestScanNumeric_RegressionUUID(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	var c Col[uuid.UUID]
	// pgx 对 uuid 列返回 [16]byte；Col[uuid.UUID] 的 Scan 走 *uuid.UUID.Scanner
	if err := c.Scan(id[:]); err != nil {
		t.Fatalf("scan [16]byte into uuid.UUID: %v", err)
	}
	if c.Get() != id {
		t.Errorf("got %v, want %v", c.Get(), id)
	}
}

// TestParseNumeric_Direct 单元测试 parseNumeric 辅助函数的 Kind 分派
func TestParseNumeric_Direct(t *testing.T) {
	rv := reflect.New(reflect.TypeOf(float64(0))).Elem()
	v, ok := parseNumeric("1.5", rv)
	if !ok || v.Float() != 1.5 {
		t.Errorf("parseNumeric float64 got %v %v", v, ok)
	}

	rvInt := reflect.New(reflect.TypeOf(int32(0))).Elem()
	_, ok = parseNumeric("1.5", rvInt) // int 不接受小数
	if ok {
		t.Error("parseNumeric int32 with '1.5' should fail")
	}

	rvStr := reflect.New(reflect.TypeOf("")).Elem()
	_, ok = parseNumeric("1", rvStr) // 非数值 Kind
	if ok {
		t.Error("parseNumeric string kind should return false")
	}
}
