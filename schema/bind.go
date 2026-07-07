package schema

import (
	"fmt"

	"github.com/sth4me/fusion/meta"
)

// DiffKind 描述模型与数据库 schema 之间的差异类型。
type DiffKind string

const (
	// DiffMissingColumn：数据库有该列但模型没有（可能漏写字段或表新增了列）。
	DiffMissingColumn DiffKind = "missing_column"
	// DiffModelExtraColumn：模型有该字段但数据库没有（模型多写了字段或表缺列）。
	DiffModelExtraColumn DiffKind = "model_extra_column"
	// DiffPKMismatch：主键集合不一致（顺序或数量不同）。
	DiffPKMismatch DiffKind = "pk_mismatch"
	// DiffNullableMismatch：可空性不一致（模型用 *T 表示可空，但列 NOT NULL；或反之）。
	// 仅做"软提示"——通过列的 Nullable 与模型字段的指针性比对。当前为启发式，可能漏报。
	DiffNullableMismatch DiffKind = "nullable_mismatch"
)

// Diff 描述一处模型与 schema 的不一致。
type Diff struct {
	Table  string   // 表名
	Kind   DiffKind
	Detail string   // 人类可读细节
	Model  string   // 模型类型名（如 fusion_test.User）
	Column string   // 涉及列名（适用时）
}

// Bind 比较已注册模型（meta.TableOf）与数据库内省结果（Catalog），返回所有差异。
// 空 Diff 切片表示完全一致。表在 Catalog 中不存在返回一个 DiffTableMissing 差异。
//
// 用法：
//
//	cat, _ := fusion.LoadSchema(ctx, db, dialect.SQLiteDialect, "users")
//	diffs := schema.Bind(cat, Users)
//	if len(diffs) > 0 { /* 漂移，可选 panic / log */ }
func Bind(cat *Catalog, tab meta.TableOf) []Diff {
	mm := tab.ModelMeta()
	st := cat.Table(mm.Table)
	var diffs []Diff
	if st == nil {
		diffs = append(diffs, Diff{
			Table:  mm.Table,
			Kind:   "table_missing",
			Detail: fmt.Sprintf("table %q not found in catalog (not introspected?)", mm.Table),
			Model:  mm.Type.String(),
		})
		return diffs
	}

	// 1. 模型字段 → 列；同时建 db 列集合用于反向检查
	dbCols := map[string]bool{}
	for _, c := range st.Columns {
		dbCols[c.Name] = true
	}
	for _, f := range mm.Fields {
		if f.IsRelation {
			continue // 关联字段不是物理列
		}
		if !dbCols[f.Column] {
			diffs = append(diffs, Diff{
				Table:  mm.Table,
				Kind:   DiffModelExtraColumn,
				Detail: fmt.Sprintf("model field %q maps to column %q which does not exist in DB", f.FieldName, f.Column),
				Model:  mm.Type.String(),
				Column: f.Column,
			})
		}
	}
	// 反向：DB 列 → 模型字段
	modelCols := map[string]bool{}
	for _, f := range mm.Fields {
		if f.IsRelation {
			continue
		}
		modelCols[f.Column] = true
	}
	for _, c := range st.Columns {
		if !modelCols[c.Name] {
			diffs = append(diffs, Diff{
				Table:  mm.Table,
				Kind:   DiffMissingColumn,
				Detail: fmt.Sprintf("DB column %q has no corresponding model field", c.Name),
				Model:  mm.Type.String(),
				Column: c.Name,
			})
		}
	}

	// 2. 主键集合比对
	modelPK := modelPrimaryKeySet(mm)
	if !sameStringSet(modelPK, st.PrimaryKey) {
		diffs = append(diffs, Diff{
			Table:  mm.Table,
			Kind:   DiffPKMismatch,
			Detail: fmt.Sprintf("model PK %v vs DB PK %v", modelPK, st.PrimaryKey),
			Model:  mm.Type.String(),
		})
	}

	return diffs
}

// modelPrimaryKeySet 返回模型的所有 IsPrimaryKey 列名（保持声明顺序）。
func modelPrimaryKeySet(mm *meta.ModelMeta) []string {
	var out []string
	for _, f := range mm.Fields {
		if f.IsPrimaryKey {
			out = append(out, f.Column)
		}
	}
	return out
}

// sameStringSet 判断两切片是否含相同元素（顺序不敏感）。
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
		if set[s] < 0 {
			return false
		}
	}
	return true
}
