package relation

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/sth4me/fusion/dialect"
	"github.com/sth4me/fusion/meta"
)

// Execer 是 Preload 执行关联查询所需的接口（与 query.QueryExecer 一致）。
// 返回标准库 *sql.Rows，由 scanRows 扫描。
type Execer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Preload 执行关联预加载（IN 批量策略，避免 N+1）。
//
// 对 parent 切片的每个元素，按 rel 的 RelMeta 查询关联数据并回填到关联字段。
// execer 复用查询的执行器（自动事务感知）；d 为方言。
//
// 流程（has_many User→Posts 为例）：
//  1. 收集所有 user 的 RefCol 值（如 user.ID）→ [1,2,3]
//  2. SELECT * FROM <关联表> WHERE <FKCol> IN (1,2,3) → []Post
//  3. 按 post.uid 分组 → map[uid][]Post
//  4. 回填每个 user.Posts（RelMany.setLoad）
//
// belongs_to 走"反向"：收集父表外键值，查引用表主键 IN，回填单值 Rel。
// m2m 走连接表两段查询。
//
// parent 必须是切片（[]T 或 *[]T），rel 必须是已注册的 RelMeta。
func Preload(ctx context.Context, execer Execer, d dialect.Dialect, parent any, rm *RelMeta) error {
	rv := reflect.ValueOf(parent)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("fusion: Preload requires a slice, got %s", rv.Kind())
	}
	n := rv.Len()
	if n == 0 {
		// 空切片：仍标记每个（无）关联为已加载（实际无操作）
		return nil
	}

	switch rm.Kind {
	case KindHasMany:
		return preloadHasMany(ctx, execer, d, rv, rm)
	case KindHasOne:
		return preloadHasOne(ctx, execer, d, rv, rm)
	case KindBelongsTo:
		return preloadBelongsTo(ctx, execer, d, rv, rm)
	case KindManyToMany:
		return preloadManyToMany(ctx, execer, d, rv, rm)
	}
	return fmt.Errorf("fusion: unknown relation kind %v", rm.Kind)
}

// PreloadPath 按点号路径递归预加载（如 ["Posts","Comments"] 等价 Preload("Posts.Comments")）。
//
// 流程（对 parent 切片）：
//  1. 第一段：Lookup RelMeta → 调 Preload 批量回填（IN 策略，同层无 N+1）。
//  2. 若只剩一段，结束。
//  3. 否则对每个父元素，取其刚回填的关联（RelMany 的内部 []T 指针 / Rel 的 *T 指针），
//     在**原对象**上递归剩余路径（深一层回填写回原对象，不丢更新）。
//
// 深层（≥2 段）采用"逐父元素子组递归"：第一层已批量，深层的 IN 范围天然是各父的子集合。
// RelMany 通过 LoadedSliceAddr 拿到原切片指针（元素可寻址，回填直达原对象）；
// Rel 通过 LoadedPtr 拿到原 *T（包装成单元素切片递归）。
func PreloadPath(ctx context.Context, execer Execer, d dialect.Dialect, parent any, path []string) error {
	if len(path) == 0 {
		return nil
	}
	rv := reflect.ValueOf(parent)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("fusion: Preload requires a slice, got %s", rv.Kind())
	}
	if rv.Len() == 0 {
		return nil
	}

	// 第一段：查父类型 + 字段名的 RelMeta
	parentType := rv.Type().Elem()
	rm := Lookup(parentType, path[0])
	if rm == nil {
		return fmt.Errorf("fusion: Preload: relation %q not registered on %s", path[0], parentType)
	}
	// 复用既有单段实现（已含 IN 批量、回填到原对象）
	if err := Preload(ctx, execer, d, parent, rm); err != nil {
		return err
	}
	if len(path) == 1 {
		return nil // 无更深段
	}

	// 对每个父元素的已加载子集合，在原对象上递归剩余路径。
	rest := path[1:]
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		relField := elem.Field(rm.FieldIndex).Addr() // *RelMany[T] / *Rel[T]
		switch rm.Kind {
		case KindHasMany, KindManyToMany:
			if err := recurseRelMany(ctx, execer, d, relField, rest); err != nil {
				return err
			}
		case KindHasOne, KindBelongsTo:
			if err := recurseRel(ctx, execer, d, relField, rest); err != nil {
				return err
			}
		}
	}
	return nil
}

// recurseRelMany 对一个 RelMany[T] 字段（已回填）的原切片递归预加载 rest 路径。
// 通过 LoadedSliceAddr 拿到 *[]T（原切片指针，元素可寻址，深层回填直达原对象）。
func recurseRelMany(ctx context.Context, execer Execer, d dialect.Dialect, relField reflect.Value, rest []string) error {
	mv := relField.MethodByName("LoadedSliceAddr")
	if !mv.IsValid() {
		return nil
	}
	results := mv.Call(nil)
	if len(results) != 1 || results[0].IsNil() {
		return nil // 未加载或空
	}
	// results[0] 是 *[]T；传给 PreloadPath（它解引用指针得可寻址切片）
	return PreloadPath(ctx, execer, d, results[0].Interface(), rest)
}

// recurseRel 对一个 Rel[T] 字段（已回填）的单值递归预加载 rest 路径。
// 通过 LoadedPtr 拿到原 *T；构造单元素切片递归，完成后把深层回填结果拷回 *T。
// （reflect 无法让两切片元素共享存储，故用"递归后拷回"保证 has_one/belongs_to 嵌套不丢更新。）
func recurseRel(ctx context.Context, execer Execer, d dialect.Dialect, relField reflect.Value, rest []string) error {
	mv := relField.MethodByName("LoadedPtr")
	if !mv.IsValid() {
		return nil
	}
	results := mv.Call(nil)
	if len(results) != 1 || results[0].IsNil() {
		return nil // 未加载或 nil
	}
	ptrVal := results[0]            // *T，指向原对象
	elemType := ptrVal.Type().Elem()
	slicePtr := reflect.New(reflect.SliceOf(elemType))
	slicePtr.Elem().Set(reflect.Append(slicePtr.Elem(), ptrVal.Elem()))
	if err := PreloadPath(ctx, execer, d, slicePtr.Interface(), rest); err != nil {
		return err
	}
	// 把深层回填后的元素拷回原对象
	ptrVal.Elem().Set(slicePtr.Elem().Index(0))
	return nil
}

// （Execer 已在上方定义，QueryContext 返回 *sql.Rows）

// collectFieldValues 从切片 rv 的每个元素，读取指定字段索引的 SQL 值（去重）。
// fieldIndex 是 Col 字段在元素结构体内的索引。
func collectFieldValues(rv reflect.Value, fieldIndex int) []any {
	seen := map[string]bool{}
	var out []any
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		col := elem.Field(fieldIndex).Addr().Interface()
		val := colSQLValue(col)
		key := fmt.Sprintf("%v", val)
		if !seen[key] {
			seen[key] = true
			out = append(out, val)
		}
	}
	return out
}

// colSQLValue 从实现了 SQLValue() 的字段（col.Col[T]）取 SQL 值。
type sqlValuer interface {
	SQLValue() (any, error)
}

func colSQLValue(field any) any {
	if sv, ok := field.(sqlValuer); ok {
		v, err := sv.SQLValue()
		if err == nil {
			return v
		}
	}
	return nil
}

// preloadHasMany：has_many（如 User→Posts）。
func preloadHasMany(ctx context.Context, execer Execer, d dialect.Dialect, rv reflect.Value, rm *RelMeta) error {
	// 1. 收集父主键值（RefCol 在父表，RefIndex 是父表字段索引）
	parentKeys := collectFieldValues(rv, rm.RefIndex)
	if len(parentKeys) == 0 {
		markRelManyLoaded(rv, rm.FieldIndex, nil)
		return nil
	}

	// 2. 查子表：SELECT * FROM <child> WHERE <FKCol> IN (...)
	childTab := meta.Lookup(rm.ChildType)
	if childTab == nil {
		return fmt.Errorf("fusion: Preload: child type %v not registered", rm.ChildType)
	}
	children, err := queryIN(ctx, execer, d, childTab, rm.FKCol, parentKeys)
	if err != nil {
		return err
	}

	// 3. 按子表外键分组（FKIndex 是子表字段索引）
	childSlice := reflect.ValueOf(children) // []Child
	groups := map[any][]reflect.Value{}
	for i := 0; i < childSlice.Len(); i++ {
		c := childSlice.Index(i)
		fk := colSQLValue(c.Field(rm.FKIndex).Addr().Interface())
		groups[fk] = append(groups[fk], c)
	}

	// 4. 回填每个父元素的 RelMany
	childSliceType := reflect.SliceOf(rm.ChildType)
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		pk := colSQLValue(elem.Field(rm.RefIndex).Addr().Interface())
		var groupVal reflect.Value
		if g, ok := groups[pk]; ok && len(g) > 0 {
			slice := reflect.MakeSlice(childSliceType, 0, len(g))
			for _, c := range g {
				slice = reflect.Append(slice, c)
			}
			groupVal = slice
		} else {
			groupVal = reflect.MakeSlice(childSliceType, 0, 0) // 空切片（加载了但无）
		}
		// 调 RelMany.setLoad([]T)
		relField := elem.Field(rm.FieldIndex).Addr()
		setLoadRelMany(relField, groupVal.Interface())
	}
	return nil
}

// preloadHasOne：has_one（如 User→Profile，外键在 profile）。
func preloadHasOne(ctx context.Context, execer Execer, d dialect.Dialect, rv reflect.Value, rm *RelMeta) error {
	parentKeys := collectFieldValues(rv, rm.RefIndex)
	if len(parentKeys) == 0 {
		markRelLoaded(rv, rm.FieldIndex, nil)
		return nil
	}
	childTab := meta.Lookup(rm.ChildType)
	if childTab == nil {
		return fmt.Errorf("fusion: Preload: child type %v not registered", rm.ChildType)
	}
	children, err := queryIN(ctx, execer, d, childTab, rm.FKCol, parentKeys)
	if err != nil {
		return err
	}
	// 按子表外键建索引（单值）
	childSlice := reflect.ValueOf(children)
	byKey := map[any]reflect.Value{}
	for i := 0; i < childSlice.Len(); i++ {
		c := childSlice.Index(i)
		fk := colSQLValue(c.Field(rm.FKIndex).Addr().Interface())
		byKey[fk] = c
	}
	// 回填每个父元素的 Rel
	childType := rm.ChildType
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		pk := colSQLValue(elem.Field(rm.RefIndex).Addr().Interface())
		relField := elem.Field(rm.FieldIndex).Addr()
		if c, ok := byKey[pk]; ok {
			ptr := reflect.New(childType)
			ptr.Elem().Set(c)
			setLoadRel(relField, ptr.Interface())
		} else {
			setLoadRel(relField, nil) // 加载了但无
		}
	}
	return nil
}

// preloadBelongsTo：belongs_to（如 User→Dept，外键在父表 user.dept_id）。
func preloadBelongsTo(ctx context.Context, execer Execer, d dialect.Dialect, rv reflect.Value, rm *RelMeta) error {
	// 1. 收集父表外键值（FKIndex 在父表，因为 belongs_to FK 在父）
	parentKeys := collectFieldValues(rv, rm.FKIndex)
	if len(parentKeys) == 0 {
		markRelLoaded(rv, rm.FieldIndex, nil)
		return nil
	}
	// 2. 查引用表：RefCol 是引用表主键
	refTab := meta.Lookup(rm.RefOwner)
	if refTab == nil {
		return fmt.Errorf("fusion: Preload: ref type %v not registered", rm.RefOwner)
	}
	children, err := queryIN(ctx, execer, d, refTab, rm.RefCol, parentKeys)
	if err != nil {
		return err
	}
	// 按引用主键建索引（单值）
	childSlice := reflect.ValueOf(children)
	byKey := map[any]reflect.Value{}
	for i := 0; i < childSlice.Len(); i++ {
		c := childSlice.Index(i)
		// 引用表的主键 = RefIndex（在 RefOwner 内）
		pk := colSQLValue(c.Field(rm.RefIndex).Addr().Interface())
		byKey[pk] = c
	}
	refType := rm.RefOwner
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		fk := colSQLValue(elem.Field(rm.FKIndex).Addr().Interface())
		relField := elem.Field(rm.FieldIndex).Addr()
		if c, ok := byKey[fk]; ok {
			ptr := reflect.New(refType)
			ptr.Elem().Set(c)
			setLoadRel(relField, ptr.Interface())
		} else {
			setLoadRel(relField, nil)
		}
	}
	return nil
}

// preloadManyToMany：m2m（User→Posts 经 user_posts 连接表）。
func preloadManyToMany(ctx context.Context, execer Execer, d dialect.Dialect, rv reflect.Value, rm *RelMeta) error {
	if rm.JoinMeta == nil {
		return fmt.Errorf("fusion: m2m missing JoinMeta")
	}
	// ChildType 为 nil 时 reflect.SliceOf(nil) 会 panic；返回 error 而非崩溃。
	// 触发条件：m2m 注册时子类型未通过 meta.Register 注册（inferChildType 返回 nil）。
	if rm.ChildType == nil {
		return fmt.Errorf("fusion: m2m Preload: child type not resolved (is the child model registered?)")
	}
	// 1. 收集父主键
	parentKeys := collectFieldValues(rv, rm.RefIndex)
	if len(parentKeys) == 0 {
		markRelManyLoaded(rv, rm.FieldIndex, nil)
		return nil
	}
	// 2. 查连接表：SELECT <RightFKCol> FROM <join> WHERE <LeftFKCol> IN (parentKeys)
	//    同时拿到 leftFK→rightFK 映射
	joinTab := meta.Lookup(rm.JoinMeta.JoinType)
	if joinTab == nil {
		return fmt.Errorf("fusion: Preload: join type %v not registered", rm.JoinMeta.JoinType)
	}
	leftToRights, err := queryJoinMap(ctx, execer, d, joinTab, rm.JoinMeta.LeftFKCol, rm.JoinMeta.RightFKCol, rm.JoinMeta.LeftFKIdx, rm.JoinMeta.RightFKIdx, parentKeys)
	if err != nil {
		return err
	}
	// 3. 收集所有 rightFK，查子表
	var allRightKeys []any
	for _, rights := range leftToRights {
		allRightKeys = append(allRightKeys, rights...)
	}
	childTab := meta.Lookup(rm.ChildType)
	if childTab == nil {
		return fmt.Errorf("fusion: Preload: child type %v not registered", rm.ChildType)
	}
	var children any
	if len(allRightKeys) > 0 {
		children, err = queryIN(ctx, execer, d, childTab, rm.RefCol, dedup(allRightKeys))
		if err != nil {
			return err
		}
	} else {
		// 空子表结果
		children = reflect.MakeSlice(reflect.SliceOf(rm.ChildType), 0, 0).Interface()
	}
	// 4. 按子表主键建索引
	childSlice := reflect.ValueOf(children)
	byKey := map[any]reflect.Value{}
	for i := 0; i < childSlice.Len(); i++ {
		c := childSlice.Index(i)
		pk := colSQLValue(c.Field(rm.RefIndex).Addr().Interface())
		byKey[pk] = c
	}
	// 5. 回填：每个父的 RelMany = 对应的 rightKeys → 子记录
	childSliceType := reflect.SliceOf(rm.ChildType)
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		pk := colSQLValue(elem.Field(rm.RefIndex).Addr().Interface())
		rightKeys := leftToRights[pk]
		slice := reflect.MakeSlice(childSliceType, 0, len(rightKeys))
		for _, rk := range rightKeys {
			if c, ok := byKey[rk]; ok {
				slice = reflect.Append(slice, c)
			}
		}
		setLoadRelMany(elem.Field(rm.FieldIndex).Addr(), slice.Interface())
	}
	return nil
}

// queryIN 执行 SELECT * FROM <table> WHERE <col> IN (...)，扫描进 []元素类型。
func queryIN(ctx context.Context, execer Execer, d dialect.Dialect, tab meta.TableOf, col string, keys []any) (any, error) {
	cm := tab.ModelMeta()
	r := &inRenderer{d: d}
	ph := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		r.idx++
		ph[i] = d.Placeholder(r.idx)
		args[i] = k
	}
	sqlStr := "SELECT " + quotedCols(cm, d) + " FROM " + d.QuoteTable(cm.Table) +
		" WHERE " + d.QuoteIdent(col) + " IN (" + strings.Join(ph, ", ") + ")"
	rows, err := execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("fusion: Preload query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	return scanRows(rows, cm, cm.Type)
}

// queryJoinMap 查连接表，返回 map[leftFK值][]rightFK值。
func queryJoinMap(ctx context.Context, execer Execer, d dialect.Dialect, tab meta.TableOf, leftCol, rightCol string, leftIdx, rightIdx int, parentKeys []any) (map[any][]any, error) {
	_ = leftIdx
	_ = rightIdx
	cm := tab.ModelMeta()
	r := &inRenderer{d: d}
	ph := make([]string, len(parentKeys))
	args := make([]any, len(parentKeys))
	for i, k := range parentKeys {
		r.idx++
		ph[i] = d.Placeholder(r.idx)
		args[i] = k
	}
	sqlStr := "SELECT " + d.QuoteIdent(leftCol) + ", " + d.QuoteIdent(rightCol) +
		" FROM " + d.QuoteTable(cm.Table) +
		" WHERE " + d.QuoteIdent(leftCol) + " IN (" + strings.Join(ph, ", ") + ")"
	rows, err := execer.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("fusion: Preload join query: %w (sql=%s)", err, sqlStr)
	}
	defer rows.Close()
	out := map[any][]any{}
	for rows.Next() {
		var lv, rv any
		if err := rows.Scan(&lv, &rv); err != nil {
			return nil, err
		}
		out[normalizeKey(lv)] = append(out[normalizeKey(lv)], normalizeKey(rv))
	}
	return out, rows.Err()
}

// scanRows 扫描 *sql.Rows 进 []elemType 的切片（复用 Col 字段的 Scanner）。
func scanRows(rows *sql.Rows, cm *meta.ModelMeta, elemType reflect.Type) (any, error) {
	// scan.All[T] 是泛型，无法直接反射调用。改用本地实现：复用 rows 扫描进 Col 字段。
	out := reflect.MakeSlice(reflect.SliceOf(elemType), 0, 4)
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	// 列名→字段索引
	idx := make([]int, len(cols))
	for i, c := range cols {
		fm := cm.FieldByColumn(c)
		if fm == nil {
			idx[i] = -1
			continue
		}
		idx[i] = fieldIndexByName(cm, fm.FieldName)
	}
	for rows.Next() {
		row := reflect.New(elemType).Elem()
		dest := make([]any, len(cols))
		for i := range cols {
			if idx[i] < 0 {
				var discard any
				dest[i] = &discard
				continue
			}
			dest[i] = row.Field(idx[i]).Addr().Interface()
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = reflect.Append(out, row)
	}
	return out.Interface(), rows.Err()
}

func fieldIndexByName(cm *meta.ModelMeta, name string) int {
	for i := 0; i < cm.Type.NumField(); i++ {
		if cm.Type.Field(i).Name == name {
			return i
		}
	}
	return -1
}

// markRelManyLoaded 把切片里所有元素的 RelMany 字段标记为已加载（空）。
func markRelManyLoaded(rv reflect.Value, fieldIndex int, _ []any) {
	for i := 0; i < rv.Len(); i++ {
		relField := rv.Index(i).Field(fieldIndex).Addr()
		setLoadRelMany(relField, nil)
	}
}

// markRelLoaded 把切片里所有元素的 Rel 字段标记为已加载（nil）。
func markRelLoaded(rv reflect.Value, fieldIndex int, _ []any) {
	for i := 0; i < rv.Len(); i++ {
		relField := rv.Index(i).Field(fieldIndex).Addr()
		setLoadRel(relField, nil)
	}
}

// setLoadRelMany 反射调用 RelMany[T].setLoad([]T)。
type relManyLoader interface {
	setLoad(v any)
}

// setLoadRelMany 调用 RelMany[T].SetLoad([]T)。relField 必须是 *RelMany[T]（已取址）。
func setLoadRelMany(relField reflect.Value, v any) {
	mv := relField.MethodByName("SetLoad")
	if !mv.IsValid() {
		return
	}
	mv.Call([]reflect.Value{reflect.ValueOf(v)})
}

// setLoadRel 调用 Rel[T].SetLoad(*T)。relField 必须是 *Rel[T]（已取址）。
func setLoadRel(relField reflect.Value, v any) {
	mv := relField.MethodByName("SetLoad")
	if !mv.IsValid() {
		return
	}
	if v == nil {
		// 构造 T 的 nil 指针（Rel.SetLoad 参数是 *T）
		elemType := relField.Type().Elem()
		if elemType.NumField() > 0 {
			ptrT := elemType.Field(0).Type // *T
			nilPtr := reflect.Zero(ptrT)
			mv.Call([]reflect.Value{nilPtr})
		}
		return
	}
	mv.Call([]reflect.Value{reflect.ValueOf(v)})
}

// inRenderer 仅用于 queryIN 占位符计数（dialect 已提供 Placeholder）。
type inRenderer struct {
	d   dialect.Dialect
	idx int
}

func (r *inRenderer) NextPlaceholder() string { r.idx++; return r.d.Placeholder(r.idx) }
func (r *inRenderer) AddParam(any)            {}
func (r *inRenderer) QuoteCol(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return r.d.QuoteIdent(s[:i]) + "." + r.d.QuoteIdent(s[i+1:])
	}
	return r.d.QuoteIdent(s)
}

// ExcludedRef 实现 expr.Renderer（preload 路径不涉及 UPSERT，返回空即可）。
func (r *inRenderer) ExcludedRef(_ string) string { return "" }

// quotedCols 返回逗号分隔的引用列名列表。
func quotedCols(cm *meta.ModelMeta, d dialect.Dialect) string {
	parts := make([]string, 0, len(cm.Fields))
	for _, f := range cm.Fields {
		if f.IsRelation {
			continue
		}
		parts = append(parts, d.QuoteIdent(f.Column))
	}
	return strings.Join(parts, ", ")
}

func dedup(vs []any) []any {
	seen := map[string]bool{}
	out := make([]any, 0, len(vs))
	for _, v := range vs {
		k := fmt.Sprintf("%v", v)
		if !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// normalizeKey 把 SQL 扫描的值归一化为可比较的 map key（int64/float64/string 等）。
func normalizeKey(v any) any {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case []byte:
		return string(x)
	case string:
		return x
	}
	return v
}
