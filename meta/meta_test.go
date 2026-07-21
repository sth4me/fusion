package meta

import (
	"reflect"
	"testing"
)

// mockDescriptor 实现 FieldDescriptor，避免依赖 col 包（防循环导入）。
type mockDescriptor struct {
	col string
}

func (m *mockDescriptor) SetMeta(fm FieldMeta) { m.col = fm.Column }

// 测试模型：用 mockDescriptor 模拟 Col 字段。
type tModel struct {
	ID        mockDescriptor
	Name      mockDescriptor
	CreatedAt mockDescriptor
	Email     mockDescriptor
}

func TestRegisterBasic(t *testing.T) {
	tab := Register[tModel]("t_model")
	if tab.Meta.Table != "t_model" {
		t.Errorf("table got %q, want t_model", tab.Meta.Table)
	}
	if tab.Meta.Type.Kind() != reflect.Struct {
		t.Errorf("type kind got %v", tab.Meta.Type.Kind())
	}
}

func TestRegisterDefaultTableSnake(t *testing.T) {
	// 用独立类型，避免被 tModel 的缓存干扰
	type freshModel struct {
		ID mockDescriptor
	}
	tab := Register[freshModel]("")
	if tab.Meta.Table != "fresh_model" {
		t.Errorf("default table got %q, want fresh_model", tab.Meta.Table)
	}
}

func TestSnakeColumns(t *testing.T) {
	tab := Register[tModel]("t_model")
	cases := map[string]string{
		"ID":        "id",
		"Name":      "name",
		"CreatedAt": "created_at",
		"Email":     "email",
	}
	for field, want := range cases {
		fm := tab.Meta.FieldByName(field)
		if fm == nil {
			t.Errorf("field %s not found", field)
			continue
		}
		if fm.Column != want {
			t.Errorf("field %s column got %q, want %q", field, fm.Column, want)
		}
	}
}

// 连续大写边界：HTTPServer → http_server，IDCard → id_card
func TestSnakeBoundaries(t *testing.T) {
	cases := map[string]string{
		"Name":       "name",
		"CreatedAt":  "created_at",
		"HTTPServer": "http_server",
		"IDCard":     "id_card",
		"ID":         "id",
		"UserID":     "user_id",
		"XYZ":        "xyz",
		"A":          "a",
	}
	for in, want := range cases {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) got %q, want %q", in, got, want)
		}
	}
}

func TestFieldByColumn(t *testing.T) {
	tab := Register[tModel]("t_model")
	fm := tab.Meta.FieldByColumn("created_at")
	if fm == nil || fm.FieldName != "CreatedAt" {
		t.Errorf("by column created_at got %v", fm)
	}
	if fm := tab.Meta.FieldByColumn("nonexistent"); fm != nil {
		t.Error("nonexistent column should return nil")
	}
}

func TestRegisterCached(t *testing.T) {
	// 同 name 二次注册：返回 cached 同指针
	t1 := Register[tModel]("t_model")
	t2 := Register[tModel]("t_model")
	if t1 != t2 {
		t.Error("repeated Register with same name should return cached Table (same pointer)")
	}

	// 不同 name 二次注册：必须 panic（fail-fast，防"同类型对应多张表"的编程错误）
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register with conflicting name should panic")
		}
	}()
	Register[tModel]("different_name")
}

func TestRegisterFillsProtoMeta(t *testing.T) {
	// Register 应把列名填进原型实例的 mockDescriptor 字段
	tab := Register[tModel]("t_model")
	if tab.Proto.Name.col != "name" {
		t.Errorf("proto Name.col got %q, want name", tab.Proto.Name.col)
	}
	if tab.Proto.CreatedAt.col != "created_at" {
		t.Errorf("proto CreatedAt.col got %q", tab.Proto.CreatedAt.col)
	}
}

func TestRegisterPanicsOnNonStruct(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register on non-struct should panic")
		}
	}()
	Register[int]("not_a_struct")
}

func TestRegisterUnexportedSkipped(t *testing.T) {
	type withPrivate struct {
		Public  mockDescriptor
		private mockDescriptor
	}
	tab := Register[withPrivate]("t")
	if len(tab.Meta.Fields) != 1 {
		t.Errorf("got %d fields, want 1 (private skipped)", len(tab.Meta.Fields))
	}
	if tab.Meta.Fields[0].FieldName != "Public" {
		t.Errorf("got field %q", tab.Meta.Fields[0].FieldName)
	}
}

func TestLookup(t *testing.T) {
	Register[tModel]("t_model")
	tab := Lookup(reflect.TypeOf(tModel{}))
	if tab == nil {
		t.Fatal("Lookup returned nil for registered type")
	}
	if Lookup(reflect.TypeOf(int(0))) != nil {
		t.Error("Lookup on unregistered type should return nil")
	}
}

// 全包装下，关联字段用标记接口识别（阶段3启用），MVP 阶段 IsRelation 应为 false。
func TestFieldMetaNotRelation(t *testing.T) {
	tab := Register[tModel]("t_model")
	for _, f := range tab.Meta.Fields {
		if f.IsRelation {
			t.Errorf("field %s should not be relation in MVP", f.FieldName)
		}
	}
}

// db 标签覆盖列名（可选特性）
func TestDBTagOverride(t *testing.T) {
	type tagged struct {
		Name mockDescriptor `db:"user_name"`
	}
	tab := Register[tagged]("t")
	fm := tab.Meta.FieldByName("Name")
	if fm == nil || fm.Column != "user_name" {
		t.Errorf("tag override got %v, want user_name", fm)
	}
}

// TestCompositePrimaryKey 多个 db:"pk" 标记 → 多个 IsPrimaryKey 字段。
func TestCompositePrimaryKey(t *testing.T) {
	type userRole struct {
		UserID mockDescriptor `db:"pk"`
		RoleID mockDescriptor `db:"pk"`
		Name   mockDescriptor
	}
	tab := Register[userRole]("t_composite_pk")
	pk := tab.Meta.PrimaryKeyColumns()
	if len(pk) != 2 {
		t.Fatalf("got %d PK cols %v, want 2", len(pk), pk)
	}
	// 按声明顺序
	if pk[0] != "user_id" || pk[1] != "role_id" {
		t.Errorf("PK cols got %v, want [user_id role_id]", pk)
	}
	// Name 不应是主键
	if fm := tab.Meta.FieldByName("Name"); fm != nil && fm.IsPrimaryKey {
		t.Error("Name should not be PK")
	}
}

// TestSinglePKFallback 无 db:"pk" 时首个非关联字段为主键（旧行为保留）。
func TestSinglePKFallback(t *testing.T) {
	type single struct {
		ID   mockDescriptor
		Name mockDescriptor
	}
	tab := Register[single]("t_single_pk")
	pk := tab.Meta.PrimaryKeyColumns()
	if len(pk) != 1 || pk[0] != "id" {
		t.Errorf("got PK %v, want [id]", pk)
	}
}
