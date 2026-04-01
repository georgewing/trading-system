// 基于 LMAX Disruptor 思想的无锁单生产者单消费者 RingBuffer
// 使用 atomic 操作替代 mutex，避免 GC 压力和锁竞争

package ringbuffer

import (
	"runtime"
)

const cacheLinePad = 64 // 缓存行大小，避免 false sharing

type paddedUint64 struct {
	val  uint64
	_pad [cacheLinePad - 8]byte
}

// RingBuffer 无锁环形缓冲区
type RingBuffer[T any] struct {
	capacity uint64
	mask     uint64

	padding0 [cacheLinePad - 8]byte

	head paddedUint64
	tail paddedUint64

	data []T
}

// New 创建容量为 2^n 的 RingBuffer（capacity 自动向上取整为 2 的幂）
func New[T any](capacity uint64) *RingBuffer[T] {
	cap := roundUpPow2(capacity)
	return &RingBuffer[T]{
		capacity: cap,
		mask:     cap - 1,
		data:     make([]T, cap),
	}
}

func roundUpPow2(v uint64) uint64 {
	if v == 0 {
		return 1
	}
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v |= v >> 32
	v++
	return v
}
