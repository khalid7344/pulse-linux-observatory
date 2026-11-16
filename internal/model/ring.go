package model

import "sync"

// RingBuffer is a generic, fixed-capacity circular buffer that overwrites
// the oldest entries when capacity is reached.
type RingBuffer[T any] struct {
	mu       sync.RWMutex
	data     []T
	capacity int
	start    int
	size     int
}

// NewRingBuffer allocates a new circular ring buffer with the given positive capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
		start:    0,
		size:     0,
	}
}

// Push adds an element to the ring buffer. If the buffer is full,
// the oldest element is overwritten in O(1) time.
func (r *RingBuffer[T]) Push(item T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size < r.capacity {
		index := (r.start + r.size) % r.capacity
		r.data[index] = item
		r.size++
	} else {
		// Buffer is full: overwrite at start, advance start
		r.data[r.start] = item
		r.start = (r.start + 1) % r.capacity
	}
}

// Slice returns all elements currently in the buffer in chronological order (oldest to newest).
func (r *RingBuffer[T]) Slice() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return nil
	}

	result := make([]T, r.size)
	for i := 0; i < r.size; i++ {
		result[i] = r.data[(r.start+i)%r.capacity]
	}
	return result
}

// Latest returns the most recently pushed item and true, or zero-value and false if empty.
func (r *RingBuffer[T]) Latest() (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		var zero T
		return zero, false
	}
	lastIdx := (r.start + r.size - 1) % r.capacity
	return r.data[lastIdx], true
}

// Len returns the current number of elements stored.
func (r *RingBuffer[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Capacity returns the maximum capacity of the buffer.
func (r *RingBuffer[T]) Capacity() int {
	return r.capacity
}

// Clear resets the buffer back to empty.
func (r *RingBuffer[T]) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.start = 0
	r.size = 0
	var zero T
	for i := range r.data {
		r.data[i] = zero
	}
}
