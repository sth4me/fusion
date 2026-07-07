package schema

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/sth4me/fusion/logging"
	"github.com/sth4me/fusion/meta"
	"github.com/sth4me/fusion/relation"
)

// AutoRegisterRelations 扫描 Catalog 中所有外键，对未手动注册的关联自动调用
// relation.BelongsTo / relation.HasMany。
//
// 关键约束（手动优先）：
//   - 注册前查 relation.Lookup(parentType, fieldName)，已存在则跳过（手写 HasMany 不被覆盖）。
//   - DB 无外键时 ForeignKeys 为空，本函数 no-op，完全退化为手写关联。
//
// 命名约定（与 relation 包 Preload 字段名一致）：
//   - belongs_to 字段名：FK 列去 "_id" 后缀后 PascalCase（dept_id → Dept）。
//     该字段须为 Rel[T] 类型（用户在模型里声明）。
//   - has_many 字段名：父表名复数化 PascalCase（user → Users）。
//     该字段须为 RelMany[T] 类型。
//
// 跳过条件（仅 log，不报错）：
//   - 复合外键（多列）：本轮不支持，记 Debug。
//   - 父/子模型未注册（meta.LookupByName 查不到）：记 Debug。
//   - 模型里找不到约定命名的关联字段：记 Debug。
func AutoRegisterRelations(cat *Catalog) {
	logger := logging.Logger()
	for _, tab := range cat.AllTables() {
		for _, fk := range tab.ForeignKeys {
			if err := registerOneFK(cat, fk, tab.Name, logger); err != nil {
				if logger != nil {
					logger.Debug("fusion: auto-relation skipped",
						"table", tab.Name, "fk_column", fk.Column, "ref_table", fk.RefTable, "reason", err.Error())
				}
			}
		}
	}
}

// registerOneFK 处理一个外键：注册 belongs_to（在子表）+ has_many（在引用表）。
func registerOneFK(cat *Catalog, fk ForeignKey, childTable string, logger Logger) error {
	// 仅处理单列外键
	if len(fk.RefColumns) != 1 {
		return fmt.Errorf("composite foreign key (cols=%v) not supported this round", fk.RefColumns)
	}
	childType := meta.LookupByTable(childTable)
	parentType := meta.LookupByTable(fk.RefTable)
	if childType == nil {
		return fmt.Errorf("child model %q not registered", childTable)
	}
	if parentType == nil {
		return fmt.Errorf("parent model %q not registered", fk.RefTable)
	}

	// belongs_to：子表 → 引用表（如 Post.Dept, FK 列 dept_id 在 Post）
	belongsToField := belongsToFieldName(fk.Column)
	if relation.Lookup(childType, belongsToField) == nil {
		if err := tryRegisterBelongsTo(childType, parentType, fk.Column, belongsToField, fk.RefColumns[0]); err != nil {
			// 字段缺失等：仅记日志，不中断
			if logger != nil {
				logger.Debug("fusion: auto-relation belongs_to skipped",
					"child", childType.String(), "field", belongsToField, "reason", err.Error())
			}
		}
	} // 已手动注册：跳过（手动优先）

	// has_many：引用表 → 子表（如 Dept.Posts）
	hasManyField := hasManyFieldName(childType)
	if relation.Lookup(parentType, hasManyField) == nil {
		if err := tryRegisterHasMany(parentType, childType, fk.Column, hasManyField, fk.RefColumns[0]); err != nil {
			if logger != nil {
				logger.Debug("fusion: auto-relation has_many skipped",
					"parent", parentType.String(), "field", hasManyField, "reason", err.Error())
			}
		}
	}
	return nil
}

// Logger 是日志接口（*log/slog.Logger 满足）。解耦避免直接依赖具体类型。
type Logger interface {
	Debug(msg string, args ...any)
}

// tryRegisterBelongsTo 合成 picker 调 relation.BelongsTo。
// 字段约定：子表上有 Rel[父表类型] 字段（名为 belongsToField），FK 列（Col）也在子表。
func tryRegisterBelongsTo(childType, parentType reflect.Type, fkCol, belongsToField, refCol string) error {
	// 子表的关联字段（Rel[parentType]）
	relIdx, err := findFieldOfTypeKind(childType, belongsToField, relKind)
	if err != nil {
		return err
	}
	// 子表的外键列字段（Col）
	fkIdx, err := findColumnField(childType, fkCol)
	if err != nil {
		return err
	}
	// 引用表主键列字段（Col）
	refIdx, err := findColumnField(parentType, refCol)
	if err != nil {
		return err
	}
	relPicker := makePicker(childType, relIdx)
	fkPicker := makePicker(childType, fkIdx)
	refPicker := makePicker(parentType, refIdx)
	relation.BelongsTo(relPicker, fkPicker, refPicker)
	return nil
}

// tryRegisterHasMany 合成 picker 调 relation.HasMany。
// 字段约定：父表上有 RelMany[子表类型] 字段（名为 hasManyField）；FK 列在子表。
func tryRegisterHasMany(parentType, childType reflect.Type, fkCol, hasManyField, refCol string) error {
	relIdx, err := findFieldOfTypeKind(parentType, hasManyField, relManyKind)
	if err != nil {
		return err
	}
	fkIdx, err := findColumnField(childType, fkCol)
	if err != nil {
		return err
	}
	refIdx, err := findColumnField(parentType, refCol)
	if err != nil {
		return err
	}
	relPicker := makePicker(parentType, relIdx)
	fkPicker := makePicker(childType, fkIdx)
	refPicker := makePicker(parentType, refIdx)
	relation.HasMany(relPicker, fkPicker, refPicker)
	return nil
}

// fieldKind 标识目标字段的描述符类型。
type autoFieldKind int

const (
	colKind autoFieldKind = iota
	relKind
	relManyKind
)

// findFieldOfTypeKind 按名找字段并验证类型前缀（col.Col[/rel.Rel[/rel.RelMany[）。
func findFieldOfTypeKind(typ reflect.Type, name string, kind autoFieldKind) (int, error) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name != name {
			continue
		}
		if !fieldMatchesKind(f.Type, kind) {
			return 0, fmt.Errorf("field %q on %s has type %s, expected %s",
				name, typ.String(), f.Type.String(), kindName(kind))
		}
		return i, nil
	}
	return 0, fmt.Errorf("field %q not found on %s (expected %s)",
		name, typ.String(), kindName(kind))
}

// findColumnField 按列名（db tag 或蛇形字段名）找 Col 字段索引。
func findColumnField(typ reflect.Type, col string) (int, error) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		// db tag 优先
		tagCol := f.Tag.Get("db")
		if tagCol != "" && tagCol != "pk" && tagCol == col {
			if fieldMatchesKind(f.Type, colKind) {
				return i, nil
			}
		}
		// 蛇形字段名匹配（用 meta.Snake 保证与列名约定一致：DeptID → dept_id）
		if meta.Snake(f.Name) == col && fieldMatchesKind(f.Type, colKind) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("column field for %q not found on %s", col, typ.String())
}

// fieldMatchesKind 按类型字符串前缀判断（避免导入 col/rel 造成循环依赖）。
func fieldMatchesKind(t reflect.Type, kind autoFieldKind) bool {
	s := t.String()
	switch kind {
	case colKind:
		return strings.HasPrefix(s, "col.Col[")
	case relKind:
		return strings.HasPrefix(s, "rel.Rel[")
	case relManyKind:
		return strings.HasPrefix(s, "rel.RelMany[")
	}
	return false
}

func kindName(k autoFieldKind) string {
	switch k {
	case colKind:
		return "col.Col[T]"
	case relKind:
		return "rel.Rel[T]"
	case relManyKind:
		return "rel.RelMany[T]"
	}
	return "unknown"
}

// makePicker 用 reflect.MakeFunc 合成 func(*T) any，返回指向第 idx 字段的指针。
// relation.resolveField 会调它拿字段指针、再反射定位字段索引。
// 注意：picker 第一参数类型必须是 reflect.PointerTo(ownerType)，否则 resolveField 的
// Kind==Ptr 校验会失败。
func makePicker(ownerType reflect.Type, fieldIdx int) any {
	ptrType := reflect.PointerTo(ownerType)
	fnType := reflect.FuncOf([]reflect.Type{ptrType}, []reflect.Type{reflect.TypeOf((*any)(nil)).Elem()}, false)
	impl := func(args []reflect.Value) []reflect.Value {
		owner := args[0]
		// owner 是 *T；取元素后取字段地址
		fieldPtr := owner.Elem().Field(fieldIdx).Addr()
		return []reflect.Value{fieldPtr}
	}
	return reflect.MakeFunc(fnType, impl).Interface()
}

// belongsToFieldName 由 FK 列名推断关联字段名：去 "_id"/"Id" 后缀 → PascalCase。
// dept_id → Dept，user_id → User。
func belongsToFieldName(fkCol string) string {
	name := fkCol
	name = strings.TrimSuffix(name, "_id")
	name = strings.TrimSuffix(name, "_guid")
	return pascalCase(name)
}

// hasManyFieldName 由子模型类型名推断父表上的 has_many 字段名。
// 约定：字段名 = 子模型类型名的复数形式（APost → APosts，User → Users）。
// 用模型类型名（而非表名）以匹配 Go 字段命名习惯。
// 复数化为简单规则（+s / y→ies / 等），不规则情况用户可手写 HasMany 覆盖。
func hasManyFieldName(childType reflect.Type) string {
	return pluralize(childType.Name())
}

// pluralize 简单复数化（覆盖常见后缀；不追求完美，用户可手动覆盖）。
func pluralize(s string) string {
	if s == "" {
		return s
	}
	switch {
	case strings.HasSuffix(s, "y") && !endsWithVowelY(s):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s") || strings.HasSuffix(s, "x") ||
		strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func endsWithVowelY(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[len(s)-2] {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// pascalCase 把 snake_case 转 PascalCase（user_dept → UserDept）。
func pascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}
