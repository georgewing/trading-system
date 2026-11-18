package datastructures

import (
	"slices"
	"testing"
)

func TestSkipList_AddRemove_NoPanic(t *testing.T) {
	sl := NewSkipList()

	prices := []int64{100, 50, 150, 25, 75, 125, 175, 10, 200, 1}
	for _, price := range prices {
		sl.Add(price, 10)
	}

	for _, price := range prices {
		if got := sl.Get(price); got != 10 {
			t.Fatalf("Get(%d) = %d, want 10", price, got)
		}
	}

	for _, price := range prices {
		sl.Remove(price, 10)
		if got := sl.Get(price); got != 0 {
			t.Fatalf("Get(%d) = %d, want 0", price, got)
		}
	}

	if sl.length != 0 {
		t.Fatalf("length = %d, want 0", sl.length)
	}
	if sl.level != 0 {
		t.Fatalf("level = %d, want 0", sl.level)
	}
	if _, ok := sl.Best(true); ok {
		t.Fatal("Best(true) on empty list = ok, want false")
	}
}

func TestSkipList_Ordering(t *testing.T) {
	sl := NewSkipList()

	input := []struct {
		price int64
		qty   int64
	}{
		{105, 1},
		{101, 2},
		{103, 3},
		{101, 4}, // 重复价位，验证 qty 累加
		{107, 5},
		{102, 6},
	}
	for _, in := range input {
		sl.Add(in.price, in.qty)
	}

	var gotPrices, gotQty []int64
	it := sl.Iterate()
	for it.Next() {
		lvl := it.Current()
		gotPrices = append(gotPrices, lvl.Price)
		gotQty = append(gotQty, lvl.Qty)
	}

	wantPrices := []int64{101, 102, 103, 105, 107}
	wantQty := []int64{6, 6, 3, 1, 5}

	if !slices.Equal(gotPrices, wantPrices) {
		t.Fatalf("prices = %v, want %v", gotPrices, wantPrices)
	}
	if !slices.Equal(gotQty, wantQty) {
		t.Fatalf("qtys = %v, want %v", gotQty, wantQty)
	}

	// Best(false) = 最低价（Ask 最佳）
	if lvl, ok := sl.Best(false); !ok || lvl.Price != 101 {
		t.Fatalf("Best(false) = %+v, %v; want price 101", lvl, ok)
	}
	// Best(true) = 最高价（Bid 最佳）
	if lvl, ok := sl.Best(true); !ok || lvl.Price != 107 {
		t.Fatalf("Best(true) = %+v, %v; want price 107", lvl, ok)
	}

	// 部分扣减
	sl.Remove(101, 2)
	if got := sl.Get(101); got != 4 {
		t.Fatalf("Get(101) after partial remove = %d, want 4", got)
	}

	// 扣完删除价位
	sl.Remove(101, 4)
	if got := sl.Get(101); got != 0 {
		t.Fatalf("Get(101) after full remove = %d, want 0", got)
	}
	if lvl, ok := sl.Best(false); !ok || lvl.Price != 102 {
		t.Fatalf("Best(false) after remove = %+v, %v; want price 102", lvl, ok)
	}
}
