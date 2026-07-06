// Package meta 管理模型元数据（字段名→列名映射、类型信息）。
//
// 全包装方案下，模型字段是 orm.Col[T]/orm.Rel[T] 等描述符类型，它们实现
// FieldDescriptor 接口。Register[T] 通过反射遍历结构体字段，调用每个字段的
// SetMeta 填充列名/表别名等元数据，并缓存到 ModelMeta。热路径零反射。
//
// 详见 docs/DESIGN.md 决策 1、2、8。
package meta

import (
	"fmt"
	"reflect"
	"sync"
	"unicode"
)

// FieldMeta 描述单个字段映射到数据库列的信息。
type FieldMeta struct {
	// FieldName 是结构体字段名（Go 名），如 "Name"。
	FieldName string
	// Column 是数据库列名（默认由字段名蛇形化得到，可被覆盖）。
	Column string
	// Table 是字段所属的数据库表名（注册时填充，不可变）。
	// 用于生成稳定的列引用 "表名.列名"，别名在 render 时由 builder 映射替换（并发安全）。
	Table string
	// IsRelation 标记是否为关联字段（Col=false，Rel/RelMany=true）。
	IsRelation bool
	// IsPrimaryKey 标记是否为主键（首个非关联字段，或 db:"pk" tag 指定）。
	IsPrimaryKey bool
}

// FieldDescriptor 由 Col[T]/Rel[T] 等字段类型实现，供 Register 反射填充元数据。
type FieldDescriptor interface {
	// SetMeta 由 Register 在反射遍历时调用，传入该字段的元数据。
	SetMeta(m FieldMeta)
}

// ModelMeta 描述一个模型的全部字段映射。
type ModelMeta struct {
	// Type 是模型类型的 reflect.Type（指针元素类型）。
	Type reflect.Type
	// Table 是数据库表名。
	Table string
	// Fields 按结构体字段顺序排列；map key 为字段名便于查找。
	Fields []FieldMeta
	byName map[string]*FieldMeta
	byCol  map[string]*FieldMeta
}

// FieldByName 按结构体字段名查找 FieldMeta，找不到返回 nil。
func (m *ModelMeta) FieldByName(name string) *FieldMeta {
	if m.byName == nil {
		return nil
	}
	return m.byName[name]
}

// FieldByColumn 按数据库列名查找 FieldMeta，找不到返回 nil。
func (m *ModelMeta) FieldByColumn(col string) *FieldMeta {
	if m.byCol == nil {
		return nil
	}
	return m.byCol[col]
}

// Table 是注册后的模型表对象，泛型持有模型类型信息，供 query 层使用。
type Table[T any] struct {
	Meta *ModelMeta
	// Proto 是填充好元数据的原型实例（用于字段描述符访问、关联定位）。
	// 字段描述符在此实例上的值已携带列名等信息。
	Proto T
}

// TableOf 是 Table[T] 的非泛型接口，便于在 map 中缓存。
type TableOf interface {
	ModelMeta() *ModelMeta
}

func (t *Table[T]) ModelMeta() *ModelMeta { return t.Meta }

// registry 全局表注册缓存。
var (
	registryMu sync.RWMutex
	registry   = make(map[reflect.Type]any) // reflect.Type(模型) -> *Table[T]
)

// Register 注册模型并返回 Table[T]。重复注册同一类型返回已缓存的 Table。
// name 为数据库表名（空则用类型名蛇形化）。
//
// Register 通过反射遍历 T 的字段：对实现 FieldDescriptor 的字段调用 SetMeta，
// 填充列名（默认蛇形化字段名）。热路径（查询/扫描）随后直接读取元数据，零反射。
func Register[T any](name string) *Table[T] {
	var zero T
	rt := reflect.TypeOf(zero)
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		panic(fmt.Sprintf("fusion: Register requires a struct, got %s", rt.Kind()))
	}

	registryMu.RLock()
	if cached, ok := registry[rt]; ok {
		registryMu.RUnlock()
		return cached.(*Table[T])
	}
	registryMu.RUnlock()

	registryMu.Lock()
	defer registryMu.Unlock()
	// double-check
	if cached, ok := registry[rt]; ok {
		return cached.(*Table[T])
	}

	if name == "" {
		name = snake(rt.Name())
	}

	t := &Table[T]{
		Meta: &ModelMeta{
			Type:   rt,
			Table:  name,
			byName: make(map[string]*FieldMeta),
			byCol:  make(map[string]*FieldMeta),
		},
	}

	// 反射遍历字段，填充元数据并写入原型实例。
	proto := reflect.New(rt).Interface().(*T) // 原型实例（指针）
	rv := reflect.ValueOf(proto).Elem()

	fields := []FieldMeta{}
	pkAssigned := false
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		// 解析 db 标签覆盖列名（可选，与"少用 tag"理念并存：默认零标签）。
		col := f.Tag.Get("db")
		// db:"pk" 标记主键（可选）；db:"colname" 指定列名
		isPK := false
		if col == "pk" {
			isPK = true
			col = snake(f.Name)
		}
		if col == "" {
			col = snake(f.Name)
		}
		fm := FieldMeta{
			FieldName:  f.Name,
			Column:     col,
			Table:      name,
			IsRelation: isRelationType(f.Type),
		}
		// 主键约定：显式 db:"pk" 优先；否则首个非关联字段为主键
		if !pkAssigned && isPK {
			fm.IsPrimaryKey = true
			pkAssigned = true
		}
		fields = append(fields, fm)
		t.Meta.byName[f.Name] = &fields[len(fields)-1]
		t.Meta.byCol[col] = &fields[len(fields)-1]

		// 若字段实现 FieldDescriptor，则向原型实例的该字段填充元数据。
		fv := rv.Field(i)
		if fv.CanAddr() {
			if d, ok := fv.Addr().Interface().(FieldDescriptor); ok {
				d.SetMeta(fm)
			}
		}
	}
	// 若无显式主键标记，首个非关联字段约定为主键
	if !pkAssigned {
		for i := range fields {
			if !fields[i].IsRelation {
				fields[i].IsPrimaryKey = true
				// 同步更新 byName/byCol 的指针（fields 是切片，元素已拷贝；需更新缓存指针指向的值）
				if ptr := t.Meta.byName[fields[i].FieldName]; ptr != nil {
					ptr.IsPrimaryKey = true
				}
				if ptr := t.Meta.byCol[fields[i].Column]; ptr != nil {
					ptr.IsPrimaryKey = true
				}
				break
			}
		}
	}
	t.Meta.Fields = fields
	// 把原型实例（值）拷入 Proto，供外部访问（字段已填充 meta）。
	t.Proto = rv.Interface().(T)

	registry[rt] = t
	return t
}

// Lookup 按 reflect.Type 查找已注册的 Table（返回非泛型接口）。
func Lookup(rt reflect.Type) TableOf {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if t, ok := registry[rt]; ok {
		return t.(TableOf)
	}
	return nil
}

// LookupByName 按类型的字符串名查找已注册的 Table。
// 用于关联注册时从 Rel[T] 的类型参数名（如 "fusion_test.Post"）反查 reflect.Type。
// 找不到返回 nil。
func LookupByName(name string) reflect.Type {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for rt := range registry {
		if typeQualifiedName(rt) == name || rt.String() == name || rt.Name() == name {
			return rt
		}
	}
	return nil
}

// LookupByTable 按数据库表名查找已注册模型的 reflect.Type。
// 用于反向迁移：从 schema 外键的引用表名（如 "posts"）反查对应 Go 类型。
// 多个模型映射同一表名时返回最先注册的一个（一般不应发生）。
// 找不到返回 nil。
func LookupByTable(tableName string) reflect.Type {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for rt, tab := range registry {
		if tab.(TableOf).ModelMeta().Table == tableName {
			return rt
		}
	}
	return nil
}

// typeQualifiedName 返回类型的全名（包路径.类型名），与 reflect.Type.String() 一致。
func typeQualifiedName(rt reflect.Type) string {
	if rt.PkgPath() == "" {
		return rt.String()
	}
	// reflect String() 形如 "fusion_test.Post"，已含包名简称；这里也提供 PkgPath.Name 形式
	return rt.PkgPath() + "." + rt.Name()
}

// RangeTables 遍历所有已注册的 Table，fn 返回 false 停止遍历。
// 供 relation 包做指针反查（当前实现未用，保留备用）。
func RangeTables(fn func(rt reflect.Type, tab TableOf) bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for rt, tab := range registry {
		if !fn(rt, tab.(TableOf)) {
			return
		}
	}
}

// isRelationType 判断字段类型是否为关联描述符（Rel/RelMany）。
// 通过接口探测，避免 meta 依赖 col/rel 包（解耦）。
func isRelationType(t reflect.Type) bool {
	// 关联描述符实现 RelationMarker 接口来表明身份（见 col/rel 包）。
	return t.Implements(relationMarkerType) || reflect.PointerTo(t).Implements(relationMarkerType)
}

// relationMarker 由关联描述符实现，供 meta 识别关联字段。
type relationMarker interface {
	_isRelation()
}

var relationMarkerType = reflect.TypeOf((*relationMarker)(nil)).Elem()

// FieldValuer 由 col.Col[T] 实现，供 DML 生成时反射收集字段值。
// meta 通过接口断言访问，避免依赖 col 包。
type fieldValuer interface {
	ColName() string
	IsSet() bool
	SQLValue() (any, error)
}

// ColEntry 是 CollectFields 返回的单个字段条目。
type ColEntry struct {
	// FieldIndex 是字段在结构体内的索引。
	FieldIndex int
	// Column 是该字段的数据库列名（来自元数据，不依赖实例是否被 Register 填充）。
	Column string
	// Valuer 是字段的 FieldValuer 接口（可读 IsSet/SQLValue）。
	Valuer fieldValuer
}

// CollectFields 遍历实例 ptr（指向已注册模型），返回所有非关联字段的 ColEntry。
// ptr 必须是指向结构体的指针。
func (m *ModelMeta) CollectFields(ptr any) []ColEntry {
	rv := reflect.ValueOf(ptr)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	out := make([]ColEntry, 0, len(m.Fields))
	for i := 0; i < m.Type.NumField(); i++ {
		fm := m.FieldByName(m.Type.Field(i).Name)
		if fm == nil || fm.IsRelation {
			continue
		}
		fv := rv.Field(i)
		if !fv.CanAddr() {
			continue
		}
		if v, ok := fv.Addr().Interface().(fieldValuer); ok {
			out = append(out, ColEntry{FieldIndex: i, Column: fm.Column, Valuer: v})
		}
	}
	return out
}

// snake 把驼峰命名转为蛇形（Name → name, CreatedAt → created_at, ID → id）。
func snake(s string) string {
	return Snake(s)
}

// Snake 是 snake 的导出版本，供 schema 等包复用，保证列名/字段名映射一致。
func Snake(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes)+4)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			// 前一个非大写，或下一个非大写（处理 IDCard → id_card 边界）
			prev := runes[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				out = append(out, '_')
			} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				// 连续大写后跟小写：如 HTTPServer → http_server
				out = append(out, '_')
			}
		}
		out = append(out, unicode.ToLower(r))
	}
	return string(out)
}
