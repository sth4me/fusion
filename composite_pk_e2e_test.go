package fusion_test

import (
	"context"
	"database/sql"
	"testing"

	"fusion"
	"fusion/col"
	"fusion/dialect"
)

// 复合主键测试模型（user_id + role_id 联合主键）。
type CPKUserRole struct {
	UserID col.Col[int64] `db:"pk"`
	RoleID col.Col[int64] `db:"pk"`
	Name   col.Col[string]
}

func setupCompositePKDB(t *testing.T) (fusion.DB, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE cpk_user_roles (user_id INTEGER, role_id INTEGER, name TEXT, PRIMARY KEY (user_id, role_id))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cpk_user_roles VALUES (1,10,'admin'),(1,20,'editor'),(2,10,'viewer')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fusion.SetDefaultDialect(dialect.SQLiteDialect)
	return fusion.WrapDB(db), db
}

// TestE2E_CompositePK_QueryGet 复合 PK 查询：按两列 Where 取单条。
func TestE2E_CompositePK_QueryGet(t *testing.T) {
	wrapped, raw := setupCompositePKDB(t)
	defer raw.Close()
	UserRoles := fusion.Register[CPKUserRole]("cpk_user_roles")

	// 按 (1, 20) 取
	ur, err := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(1).And(UserRoles.Proto.RoleID.Eq(20))).
		One(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if ur.Name.Get() != "editor" {
		t.Errorf("got name %q, want editor", ur.Name.Get())
	}
}

// TestE2E_CompositePK_UpdateNoWhere 无 Where 自动按复合 PK 更新。
func TestE2E_CompositePK_UpdateNoWhere(t *testing.T) {
	wrapped, raw := setupCompositePKDB(t)
	defer raw.Close()
	UserRoles := fusion.Register[CPKUserRole]("cpk_user_roles")

	// 加载 (1,10)，改名后无 Where 更新（应自动按 user_id+role_id 定位）
	ur, _ := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(1).And(UserRoles.Proto.RoleID.Eq(10))).
		One(context.Background())
	ur.Name.Set("superadmin")
	if err := fusion.Update(UserRoles, wrapped, &ur).Exec(context.Background()); err != nil {
		t.Fatalf("update: %v", err)
	}

	// 验证只改了 (1,10)，(1,20) 不受影响
	got, _ := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(1).And(UserRoles.Proto.RoleID.Eq(10))).
		One(context.Background())
	if got.Name.Get() != "superadmin" {
		t.Errorf("(1,10) name got %q, want superadmin", got.Name.Get())
	}
	other, _ := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(1).And(UserRoles.Proto.RoleID.Eq(20))).
		One(context.Background())
	if other.Name.Get() != "editor" {
		t.Errorf("(1,20) name got %q, should be unchanged (editor)", other.Name.Get())
	}
}

// TestE2E_CompositePK_DeleteByIDs 复合 PK 删除。
func TestE2E_CompositePK_DeleteByIDs(t *testing.T) {
	wrapped, raw := setupCompositePKDB(t)
	defer raw.Close()
	UserRoles := fusion.Register[CPKUserRole]("cpk_user_roles")

	// 删除 (1, 20)
	if err := fusion.DeleteByIDs(UserRoles, wrapped,
		map[string]any{"user_id": int64(1), "role_id": int64(20)}).
		Exec(context.Background()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// (1,20) 应不存在
	all, _ := fusion.From(UserRoles, wrapped).All(context.Background())
	for _, u := range all {
		if u.UserID.Get() == 1 && u.RoleID.Get() == 20 {
			t.Error("(1,20) should be deleted")
		}
	}
	// 其余 2 条仍在
	if len(all) != 2 {
		t.Errorf("after delete got %d rows, want 2", len(all))
	}
}

// TestE2E_CompositePK_Insert 复合 PK 插入（两列都显式提供）。
func TestE2E_CompositePK_Insert(t *testing.T) {
	wrapped, raw := setupCompositePKDB(t)
	defer raw.Close()
	UserRoles := fusion.Register[CPKUserRole]("cpk_user_roles")

	ur := &CPKUserRole{}
	ur.UserID.Set(3)
	ur.RoleID.Set(30)
	ur.Name.Set("newcomer")
	if err := fusion.Insert(UserRoles, wrapped, ur).Exec(context.Background()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := fusion.From(UserRoles, wrapped).
		Where(UserRoles.Proto.UserID.Eq(3).And(UserRoles.Proto.RoleID.Eq(30))).
		One(context.Background())
	if err != nil {
		t.Fatalf("query after insert: %v", err)
	}
	if got.Name.Get() != "newcomer" {
		t.Errorf("got name %q, want newcomer", got.Name.Get())
	}
}
