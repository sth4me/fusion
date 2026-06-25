package col

import "testing"

type pCol[T any] struct{ // 复用 miniR
}

// 复用 col_extra_test.go 的 r2 renderer
func TestSelectItemAs(t *testing.T) {
	var c Col[string]
	c.col, c.table = "name", "t0"
	item := c.As("user_name")
	r := &r2{}
	got := item.RenderSelect(r)
	want := "`t0`.`name` AS user_name"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSelectItemAlias(t *testing.T) {
	var c Col[int]
	c.col = "age"
	item := c.As("yrs")
	if item.Alias() != "yrs" {
		t.Errorf("got %q", item.Alias())
	}
}

func TestCountStar(t *testing.T) {
	item := Count[int]()
	r := &r2{}
	got := item.RenderSelect(r)
	if got != "COUNT(*)" {
		t.Errorf("got %q, want COUNT(*)", got)
	}
}

func TestCountCol(t *testing.T) {
	var c Col[int]
	c.col, c.table = "id", "t0"
	item := Count[int](c)
	r := &r2{}
	got := item.RenderSelect(r)
	want := "COUNT(`t0`.`id`)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCountAs(t *testing.T) {
	item := Count[int]().As("cnt")
	r := &r2{}
	got := item.RenderSelect(r)
	if got != "COUNT(*) AS cnt" {
		t.Errorf("got %q", got)
	}
}

func TestSumAvgMinMax(t *testing.T) {
	var c Col[int64]
	c.col, c.table = "price", "t0"
	r := &r2{}

	cases := []struct {
		name string
		item SelectItem
		want string
	}{
		{"Sum", Sum(c), "SUM(`t0`.`price`)"},
		{"Avg", Avg(c), "AVG(`t0`.`price`)"},
		{"Min", Min(c), "MIN(`t0`.`price`)"},
		{"Max", Max(c), "MAX(`t0`.`price`)"},
	}
	for _, tc := range cases {
		if got := tc.item.RenderSelect(r); got != tc.want {
			t.Errorf("%s got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSumAs(t *testing.T) {
	var c Col[int64]
	c.col = "price"
	item := Sum(c).As("total")
	r := &r2{}
	got := item.RenderSelect(r)
	if got != "SUM(`price`) AS total" {
		t.Errorf("got %q", got)
	}
}
