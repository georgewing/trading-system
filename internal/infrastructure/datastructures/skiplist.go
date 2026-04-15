package datastructures

import (
	"math/rand"
	"sync"
	"time"
)

const (
	maxLevel    = 32  // 支持百万级价格档位
	probability = 0.5 // 经典概率，平均层高 log_{1/p} N ≈ 4~5 层
)

type PriceLevel struct {
	Price int64
	Qty   int64

	forward []*PriceLevel
}

// SkipList 实现价格档位的有序存储（升序排列）
type Skiplist struct {
	header *PriceLevel
	level  int
	length int
	rand   *rand.Rand
	mutex  sync.RWMutex
}

// NewSkiplist 创建一个新的 SkipList
func NewSkiplist() *Skiplist {
	return &Skiplist{
		header: &PriceLevel{
			forward: make([]*PriceLevel, maxLevel),
		},
		level: 0,
		rand:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// randomLevel 生成随机层高（概率递减）
func (sl *Skiplist) randomLevel() int {
	level := 0
	for level < maxLevel && sl.rand.Float64() < probability {
		level++
	}
	return level
}

// Add 增加指定价格档位的数量（qty为正数）
func (sl *Skiplist) Add(price, qty int64) {
	// 若价格已存在，则累加数量；若qty <=0，则忽略
	if qty <= 0 {
		return
	}

	sl.mutex.Lock()
	defer sl.mutex.Unlock()

	update := make([]*PriceLevel, maxLevel)
	current := sl.header

	// 查找插入位置
	for i := sl.level; i >= 0; i-- {
		for current.forward[i] != nil && current.forward[i].Price < price {
			current = current.forward[i]
		}
		update[i] = current
	}

	// 若已存在相同价格，累加数量
	if current.forward[0] != nil && current.forward[0].Price == price {
		current.forward[0].Qty += qty
		return
	}

	newLevel := sl.randomLevel()
	if newLevel > sl.level {
		for i := sl.level + 1; i <= newLevel; i++ {
			update[i] = sl.header
		}
		sl.level = newLevel
	}

	n := &PriceLevel{
		Price:   price,
		Qty:     qty,
		forward: make([]*PriceLevel, newLevel),
	}

	for i := 0; i < newLevel; i++ {
		n.forward[i] = update[i].forward[i]
		update[i].forward[i] = n
	}
	sl.length++
}

// Remove 扣减数量
func (sl *Skiplist) Remove(price, qty int64) {
	if qty <= 0 {
		return
	}

	sl.mutex.Lock()
	defer sl.mutex.Unlock()

	update := make([]*PriceLevel, maxLevel)
	current := sl.header

	for i := sl.level; i >= 0; i-- {
		for current.forward[i] != nil && current.forward[i].Price < price {
			current = current.forward[i]
		}
		update[i] = current
	}

	if current.forward[0] != nil && current.forward[0].Price == price {
		target := current.forward[0]
		target.Qty -= qty

		// 扣减后 <= 0 则自动删除档位
		if target.Qty <= 0 {
			for i := 0; i <= sl.level; i++ {
				if update[i].forward[i] == target {
					update[i].forward[i] = target.forward[i]
				}
			}

			for sl.level > 0 && sl.header.forward[sl.level] == nil {
				sl.level--
			}
			sl.length--
		}
	}
}

// Get 返回指定价格的剩余数量（不存在返回 0）
func (sl *Skiplist) Get(price int64) int64 {
	sl.mutex.RLock()
	defer sl.mutex.RUnlock()

	current := sl.header
	for i := sl.level; i >= 0; i-- {
		for current.forward[i] != nil && current.forward[i].Price < price {
			current = current.forward[i]
		}
	}

	if current.forward[0] != nil && current.forward[0].Price == price {
		return current.forward[0].Qty
	}
	return 0
}

// best bid/ask
func (sl *Skiplist) Best(price float64, qty float64) bool {

}

type Iterate struct {
	current *PriceLevel
}

// 用于生成 depth/market-by-price
func (sl *Skiplist) Iterate() Iterate {
	return Iterate{current: sl.header}
}
