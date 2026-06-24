package relation

import (
	"reflect"
	"testing"

	"fusion/col"
	"fusion/meta"
	"fusion/rel"
)

// 测试模型
type tUser struct {
	ID      col.Col[int64]
	DeptID  col.Col[int64]
	Dept    rel.Rel[tDept]
	Posts   rel.RelMany[tPost]
	Profile rel.Rel[tProfile]
}

type tDept struct {
	ID   col.Col[int64]
	Name col.Col[string]
}

type tPost struct {
	ID     col.Col[int64]
	UID    col.Col[int64]
	Title  col.Col[string]
}

type tProfile struct {
	ID    col.Col[int64]
	UID   col.Col[int64] // has_one：外键在 profile
	Bio   col.Col[string]
}

// UserPost m2m 连接表
type tUserPost struct {
	UserID col.Col[int64]
	PostID col.Col[int64]
}

func init() {
	// 注册模型（顺序：先子表，让 LookupByName 能找到）
	meta.Register[tDept]("depts")
	meta.Register[tPost]("posts")
	meta.Register[tProfile]("profiles")
	meta.Register[tUserPost]("user_posts")
	meta.Register[tUser]("users")
}

func TestResolveFieldCol(t *testing.T) {
	// 取一个 Col 字段
	fi := resolveField(func(u *tUser) any { return &u.DeptID })
	if fi.name != "DeptID" {
		t.Errorf("name got %q, want DeptID", fi.name)
	}
	if !fi.isCol {
		t.Error("DeptID should be Col")
	}
	if fi.colName != "dept_id" {
		t.Errorf("colName got %q, want dept_id", fi.colName)
	}
	if fi.ownerType != reflect.TypeOf(tUser{}) {
		t.Errorf("ownerType got %v", fi.ownerType)
	}
}

func TestResolveFieldRel(t *testing.T) {
	fi := resolveField(func(u *tUser) any { return &u.Dept })
	if fi.name != "Dept" {
		t.Errorf("name got %q, want Dept", fi.name)
	}
	if !fi.isRel {
		t.Error("Dept should be Rel")
	}
}

func TestResolveFieldRelMany(t *testing.T) {
	fi := resolveField(func(u *tUser) any { return &u.Posts })
	if fi.name != "Posts" {
		t.Errorf("name got %q, want Posts", fi.name)
	}
	if !fi.isRelMany {
		t.Error("Posts should be RelMany")
	}
}

func TestHasMany(t *testing.T) {
	rm := HasMany(
		func(u *tUser) any { return &u.Posts },
		func(p *tPost) any { return &p.UID },
		func(u *tUser) any { return &u.ID },
	)
	if rm.Kind != KindHasMany {
		t.Errorf("Kind got %v, want KindHasMany", rm.Kind)
	}
	if rm.ParentType != reflect.TypeOf(tUser{}) {
		t.Errorf("ParentType got %v", rm.ParentType)
	}
	if rm.ChildType != reflect.TypeOf(tPost{}) {
		t.Errorf("ChildType got %v", rm.ChildType)
	}
	if !rm.FieldIsRelMany {
		t.Error("Posts should be RelMany")
	}
	if !rm.FKIsOnChild {
		t.Error("has_many FK should be on child")
	}
	if rm.FKCol != "uid" {
		t.Errorf("FKCol got %q, want uid", rm.FKCol)
	}
	if rm.RefCol != "id" {
		t.Errorf("RefCol got %q, want id", rm.RefCol)
	}
}

func TestBelongsTo(t *testing.T) {
	rm := BelongsTo(
		func(u *tUser) any { return &u.Dept },
		func(u *tUser) any { return &u.DeptID },
		func(d *tDept) any { return &d.ID },
	)
	if rm.Kind != KindBelongsTo {
		t.Errorf("Kind got %v", rm.Kind)
	}
	if rm.FKIsOnChild {
		t.Error("belongs_to FK should be on parent (not child)")
	}
	if rm.FKCol != "dept_id" {
		t.Errorf("FKCol got %q, want dept_id", rm.FKCol)
	}
	// belongs_to 的 FKOwner 是父表 tUser
	if rm.FKOwner != reflect.TypeOf(tUser{}) {
		t.Errorf("FKOwner got %v, want tUser", rm.FKOwner)
	}
	// RefOwner 是引用表 tDept
	if rm.RefOwner != reflect.TypeOf(tDept{}) {
		t.Errorf("RefOwner got %v, want tDept", rm.RefOwner)
	}
}

func TestHasOne(t *testing.T) {
	rm := HasOne(
		func(u *tUser) any { return &u.Profile },
		func(p *tProfile) any { return &p.UID },
		func(u *tUser) any { return &u.ID },
	)
	if rm.Kind != KindHasOne {
		t.Errorf("Kind got %v", rm.Kind)
	}
	if rm.FieldIsRelMany {
		t.Error("Profile should be Rel (not RelMany)")
	}
	if rm.FKCol != "uid" {
		t.Errorf("FKCol got %q, want uid", rm.FKCol)
	}
}

func TestManyToMany(t *testing.T) {
	rm := ManyToMany(
		func(u *tUser) any { return &u.Posts },
		func(j *tUserPost) any { return &j.UserID },
		func(j *tUserPost) any { return &j.PostID },
		func(u *tUser) any { return &u.ID },
		func(p *tPost) any { return &p.ID },
	)
	if rm.Kind != KindManyToMany {
		t.Errorf("Kind got %v", rm.Kind)
	}
	if rm.JoinMeta == nil {
		t.Fatal("JoinMeta should not be nil")
	}
	if rm.JoinMeta.LeftFKCol != "user_id" {
		t.Errorf("LeftFKCol got %q, want user_id", rm.JoinMeta.LeftFKCol)
	}
	if rm.JoinMeta.RightFKCol != "post_id" {
		t.Errorf("RightFKCol got %q, want post_id", rm.JoinMeta.RightFKCol)
	}
}

func TestLookup(t *testing.T) {
	HasMany(
		func(u *tUser) any { return &u.Posts },
		func(p *tPost) any { return &p.UID },
		func(u *tUser) any { return &u.ID },
	)
	rm := Lookup(reflect.TypeOf(tUser{}), "Posts")
	if rm == nil {
		t.Fatal("Lookup should find registered HasMany")
	}
	if rm.Kind != KindHasMany {
		t.Errorf("Kind got %v", rm.Kind)
	}
	// 查不存在的
	if Lookup(reflect.TypeOf(tUser{}), "Nonexistent") != nil {
		t.Error("nonexistent field should return nil")
	}
}

func TestAllRelations(t *testing.T) {
	// 注册两个关联
	HasMany(func(u *tUser) any { return &u.Posts },
		func(p *tPost) any { return &p.UID },
		func(u *tUser) any { return &u.ID })
	BelongsTo(func(u *tUser) any { return &u.Dept },
		func(u *tUser) any { return &u.DeptID },
		func(d *tDept) any { return &d.ID })

	all := AllRelations(reflect.TypeOf(tUser{}))
	if len(all) < 2 {
		t.Errorf("got %d relations, want >= 2", len(all))
	}
}

func TestInferChildType(t *testing.T) {
	fi := resolveField(func(u *tUser) any { return &u.Posts })
	ct := inferChildType(fi)
	if ct != reflect.TypeOf(tPost{}) {
		t.Errorf("child type got %v, want tPost", ct)
	}
}

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		KindBelongsTo:  "BelongsTo",
		KindHasOne:     "HasOne",
		KindHasMany:    "HasMany",
		KindManyToMany: "ManyToMany",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d.String() got %q, want %q", k, got, want)
		}
	}
}
