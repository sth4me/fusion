package hook

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type tModel struct{ ID int }

// TestRegisterTrigger 验证注册与触发
func TestRegisterTrigger(t *testing.T) {
	var got []string
	unreg := Register((*tModel)(nil), BeforeCreate, func(ctx context.Context, target any) error {
		got = append(got, "before")
		return nil
	})
	defer unreg()

	unreg2 := Register((*tModel)(nil), AfterCreate, func(ctx context.Context, target any) error {
		got = append(got, "after")
		return nil
	})
	defer unreg2()

	m := &tModel{ID: 1}
	if err := Trigger(context.Background(), m, BeforeCreate); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "before" {
		t.Errorf("got %v", got)
	}

	if err := Trigger(context.Background(), m, AfterCreate); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "after" {
		t.Errorf("got %v", got)
	}
}

// TestHookStopsOnError 验证 Before 钩子返回 error 时中止后续
func TestHookStopsOnError(t *testing.T) {
	calls := []string{}
	Register((*tModel)(nil), BeforeCreate, func(ctx context.Context, target any) error {
		calls = append(calls, "first")
		return nil
	})
	Register((*tModel)(nil), BeforeCreate, func(ctx context.Context, target any) error {
		calls = append(calls, "second-error")
		return errors.New("stop")
	})
	Register((*tModel)(nil), BeforeCreate, func(ctx context.Context, target any) error {
		calls = append(calls, "third-should-not-run")
		return nil
	})

	err := Trigger(context.Background(), &tModel{}, BeforeCreate)
	if err == nil {
		t.Fatal("should return error")
	}
	if len(calls) != 2 || calls[1] != "second-error" {
		t.Errorf("calls got %v, want first+second-error (third skipped)", calls)
	}
}

func TestUnregister(t *testing.T) {
	called := false
	unreg := Register((*tModel)(nil), BeforeUpdate, func(ctx context.Context, target any) error {
		called = true
		return nil
	})
	Trigger(context.Background(), &tModel{}, BeforeUpdate)
	if !called {
		t.Error("hook should fire before unregister")
	}

	called = false
	unreg()
	Trigger(context.Background(), &tModel{}, BeforeUpdate)
	if called {
		t.Error("hook should not fire after unregister")
	}
}

// TestTriggerByType 验证按类型触发（无实例，如 Delete）
func TestTriggerByType(t *testing.T) {
	fired := false
	Register((*tModel)(nil), BeforeDelete, func(ctx context.Context, target any) error {
		fired = true
		if target != nil {
			t.Error("target should be nil for type-based trigger")
		}
		return nil
	})
	err := TriggerByType(context.Background(), reflect.TypeOf(tModel{}), BeforeDelete, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Error("TriggerByType should fire registered hook")
	}
}

// TestIsolatedByType 验证不同模型类型的钩子互不干扰
func TestIsolatedByType(t *testing.T) {
	type other struct{}
	modelFired := false
	otherFired := false
	Register((*tModel)(nil), AfterCreate, func(ctx context.Context, target any) error {
		modelFired = true
		return nil
	})
	Register((*other)(nil), AfterCreate, func(ctx context.Context, target any) error {
		otherFired = true
		return nil
	})

	Trigger(context.Background(), &tModel{}, AfterCreate)
	if !modelFired {
		t.Error("tModel hook should fire")
	}
	if otherFired {
		t.Error("other hook should NOT fire for tModel")
	}
}
