// Package relation 提供关联关系注册（belongs_to/has_one/has_many/many_to_many）。
//
// 注册采用「全局注册 + 字段指针定位」（文档 #2）：传入回调函数取字段地址，
// 反射算出字段在结构体内的索引、列名，建立 RelMeta 存进 registry。
// Preload 时（见 Preload 函数）按 RelMeta 执行 IN 批量查询并回填。
//
// 回调式取字段（func(u *User) any { return &u.Posts }）完全类型安全、零字符串，
// 且能可靠拿到字段索引（回调内 u 是真实结构体指针）。
//
// 详见 docs/DESIGN.md 决策 5、6、#2、#7。
package relation

import (
	"reflect"
	"sync"

	"github.com/sth4me/fusion/meta"
)

// Kind 是关联类型。
type Kind int

const (
	KindBelongsTo Kind = iota
	KindHasOne
	KindHasMany
	KindManyToMany
)

// String 返回关联类型名。
func (k Kind) String() string {
	switch k {
	case KindBelongsTo:
		return "BelongsTo"
	case KindHasOne:
		return "HasOne"
	case KindHasMany:
		return "HasMany"
	case KindManyToMany:
		return "ManyToMany"
	}
	return "Unknown"
}

// fieldInfo 是回调取字段后的解析结果。
type fieldInfo struct {
	ownerType reflect.Type // 字段所属结构体类型（元素类型）
	name      string       // 字段名
	index     int          // 字段在结构体内的索引
	colName   string       // 列名（若字段是 Col[T]，否则空）
	isCol     bool         // 是否为 Col[T]（普通字段）
	isRel     bool         // 是否为 Rel[T]
	isRelMany bool         // 是否为 RelMany[T]
}

// resolveField 执行回调拿到字段指针，反射解析出 fieldInfo。
// picker 创建一个零值结构体，回调返回某字段地址；反射比对定位。
func resolveField(picker any) fieldInfo {
	// picker 形如 func(u *User) any { return &u.Posts }
	pv := reflect.ValueOf(picker)
	if pv.Kind() != reflect.Func {
		panic("fusion: relation picker must be a func(*T) any")
	}
	pt := pv.Type()
	if pt.NumIn() != 1 || pt.In(0).Kind() != reflect.Ptr {
		panic("fusion: relation picker must be func(*T) any")
	}
	ownerType := pt.In(0).Elem()
	// 构造一个 ownerType 实例，调 picker 拿字段指针
	inst := reflect.New(ownerType)
	got := pv.Call([]reflect.Value{inst})[0]
	if !got.IsValid() {
		panic("fusion: relation picker returned invalid value")
	}
	// 回调返回 any，可能是 *Col/*Rel/*RelMany 装在接口里；解接口取真实值。
	for got.Kind() == reflect.Interface {
		got = got.Elem()
	}
	if got.Kind() != reflect.Ptr {
		panic("fusion: relation picker must return a field pointer")
	}
	// got 是指向 ownerType 某字段的指针。通过 unsafe 算偏移定位字段索引。
	instBase := inst.Pointer()
	fieldPtr := got.Pointer()
	if fieldPtr < instBase {
		panic("fusion: relation picker returned invalid field pointer")
	}
	offset := fieldPtr - instBase
	// 遍历字段找 offset 匹配
	idx := -1
	for i := 0; i < ownerType.NumField(); i++ {
		if ownerType.Field(i).Offset == offset {
			idx = i
			break
		}
	}
	if idx < 0 {
		panic("fusion: relation field not found by offset")
	}
	f := ownerType.Field(idx)
	fi := fieldInfo{
		ownerType: ownerType,
		name:      f.Name,
		index:     idx,
	}
	// 判断字段类型（按类型字符串前缀，避免导入 col/rel 包循环）
	typeStr := f.Type.String()
	switch {
	case hasPrefix(typeStr, "col.Col["):
		fi.isCol = true
		fi.colName = lookupColumn(ownerType, f.Name)
	case hasPrefix(typeStr, "rel.Rel["):
		fi.isRel = true
	case hasPrefix(typeStr, "rel.RelMany["):
		fi.isRelMany = true
	}
	return fi
}

// lookupColumn 从已注册模型元数据取列名（蛇形或 db tag）。
func lookupColumn(ownerType reflect.Type, fieldName string) string {
	tab := meta.Lookup(ownerType)
	if tab == nil {
		return ""
	}
	fm := tab.ModelMeta().FieldByName(fieldName)
	if fm == nil {
		return ""
	}
	return fm.Column
}

// RelMeta 描述一个关联关系。Preload 时据此执行查询与回填。
type RelMeta struct {
	Kind       Kind
	ParentType reflect.Type // 父模型元素类型（如 User）
	ChildType  reflect.Type // 子模型元素类型（如 Post）
	FieldIndex int          // 关联字段（Rel/RelMany）在父结构体内的索引
	FieldIsRelMany bool     // true=RelMany（has_many/m2m）

	// 外键：belongs_to 时 FK 在父表（FKOwner=ParentType）；has_* 时在子表（FKOwner=ChildType）
	FKIsOnChild bool          // 外键是否在子表（has_one/has_many=true，belongs_to=false）
	FKOwner     reflect.Type  // 外键所在结构体类型
	FKCol       string        // 外键列名
	FKIndex     int           // 外键字段在 FKOwner 结构体内的索引
	RefOwner    reflect.Type  // 引用键所在结构体类型
	RefCol      string        // 引用键列名
	RefIndex    int           // 引用键字段在 RefOwner 结构体内的索引

	// m2m 额外
	JoinMeta *JoinMeta
}

// JoinMeta 描述多对多连接表信息（文档 #7 强制建模 B）。
type JoinMeta struct {
	JoinType   reflect.Type // 连接表元素类型
	LeftFKCol  string
	LeftFKIdx  int
	RightFKCol string
	RightFKIdx int
}

type regKey struct {
	parent reflect.Type
	field  string
}

var (
	mu       sync.RWMutex
	registry = make(map[regKey]*RelMeta)
)

// HasMany 注册一对多关联。
//   - relField: func(u *User) any { return &u.Posts } —— 父集合字段
//   - childFK:  func(p *Post) any { return &p.UID } —— 子表外键字段
//   - parentRef: func(u *User) any { return &u.ID } —— 父主键字段
func HasMany(relField, childFK, parentRef any) *RelMeta {
	return doRegister(KindHasMany, relField, childFK, parentRef)
}

// HasOne 注册一对一关联。
func HasOne(relField, childFK, parentRef any) *RelMeta {
	return doRegister(KindHasOne, relField, childFK, parentRef)
}

// BelongsTo 注册多对一关联。
//   - relField:  func(u *User) any { return &u.Dept }
//   - parentFK:  func(u *User) any { return &u.DeptID } —— 父表外键字段
//   - ref:       func(d *Dept) any { return &d.ID } —— 引用表主键
func BelongsTo(relField, parentFK, ref any) *RelMeta {
	rm := doRegister(KindBelongsTo, relField, parentFK, ref)
	rm.FKIsOnChild = false // 覆盖：belongs_to 的 FK 在父表
	return rm
}

// doRegister 三元组注册（relField, fkField, refField）。
// 默认 FKIsOnChild=true（has_one/has_many）；belongs_to 调用后覆盖。
func doRegister(kind Kind, relField, fkField, refField any) *RelMeta {
	rf := resolveField(relField)
	fk := resolveField(fkField)
	ref := resolveField(refField)

	// 推断子类型
	childType := inferChildType(rf)

	rm := &RelMeta{
		Kind:           kind,
		ParentType:     rf.ownerType,
		ChildType:      childType,
		FieldIndex:     rf.index,
		FieldIsRelMany: rf.isRelMany,
		FKIsOnChild:    true, // 默认；belongs_to 覆盖
		FKOwner:        fk.ownerType,
		FKCol:          fk.colName,
		FKIndex:        fk.index,
		RefOwner:       ref.ownerType,
		RefCol:         ref.colName,
		RefIndex:       ref.index,
	}

	mu.Lock()
	registry[regKey{rf.ownerType, rf.name}] = rm
	mu.Unlock()
	return rm
}

// ManyToMany 注册多对多关联（文档 #7 强制建模 B）。
//   - relField:    func(u *User) any { return &u.Posts }
//   - joinLeftFK:  func(j *UserPost) any { return &j.UserID } —— 连接表指向父的外键
//   - joinRightFK: func(j *UserPost) any { return &j.PostID } —— 连接表指向子的外键
//   - parentRef:   func(u *User) any { return &u.ID }
//   - childRef:    func(p *Post) any { return &p.ID }
func ManyToMany(relField, joinLeftFK, joinRightFK, parentRef, childRef any) *RelMeta {
	rf := resolveField(relField)
	lf := resolveField(joinLeftFK)
	rt := resolveField(joinRightFK)
	pr := resolveField(parentRef)
	cr := resolveField(childRef)

	rm := &RelMeta{
		Kind:           KindManyToMany,
		ParentType:     rf.ownerType,
		ChildType:      inferChildType(rf),
		FieldIndex:     rf.index,
		FieldIsRelMany: true,
		FKCol:          lf.colName, // Preload 用连接表左外键收集父 ID
		FKIndex:        lf.index,
		RefCol:         cr.colName,
		RefIndex:       cr.index,
		JoinMeta: &JoinMeta{
			JoinType:   lf.ownerType,
			LeftFKCol:  lf.colName,
			LeftFKIdx:  lf.index,
			RightFKCol: rt.colName,
			RightFKIdx: rt.index,
		},
	}
	_ = pr
	mu.Lock()
	registry[regKey{rf.ownerType, rf.name}] = rm
	mu.Unlock()
	return rm
}

// inferChildType 从 rel 字段类型（Rel[T]/RelMany[T]）提取 T。
func inferChildType(rf fieldInfo) reflect.Type {
	// rf.ownerType.Field(rf.index).Type 形如 rel.RelMany[Post]
	ft := rf.ownerType.Field(rf.index).Type
	s := ft.String()
	// 解析 "rel.RelMany[X]" 或 "rel.Rel[X]" 中的类型名 X
	for _, prefix := range []string{"rel.RelMany[", "rel.Rel["} {
		if i := indexOf(s, prefix); i >= 0 {
			rest := s[i+len(prefix):]
			if j := lastIndexByte(rest, ']'); j >= 0 {
				rest = rest[:j]
				return meta.LookupByName(rest)
			}
		}
	}
	return nil
}

// Lookup 查询某父类型 + 字段名的 RelMeta。
func Lookup(parentType reflect.Type, fieldName string) *RelMeta {
	mu.RLock()
	defer mu.RUnlock()
	return registry[regKey{parentType, fieldName}]
}

// AllRelations 返回某父类型的全部关联（用于 Preload 嵌套）。
func AllRelations(parentType reflect.Type) []*RelMeta {
	mu.RLock()
	defer mu.RUnlock()
	var out []*RelMeta
	for k, v := range registry {
		if k.parent == parentType {
			out = append(out, v)
		}
	}
	return out
}

// 辅助
func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
