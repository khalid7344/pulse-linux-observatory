package model

import (
	"sync"
	"testing"
)

func TestRingBuffer_Basic(t *testing.T) {
	rb := NewRingBuffer[int](3)
	if rb.Capacity() != 3 {
		t.Fatalf("expected capacity 3, got %d", rb.Capacity())
	}
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
	if _, ok := rb.Latest(); ok {
		t.Fatalf("expected Latest() on empty buffer to be false")
	}
	if rb.Slice() != nil {
		t.Fatalf("expected nil slice on empty buffer")
	}

	rb.Push(10)
	rb.Push(20)
	if rb.Len() != 2 {
		t.Fatalf("expected len 2, got %d", rb.Len())
	}
	val, ok := rb.Latest()
	if !ok || val != 20 {
		t.Fatalf("expected latest 20, got %v (%v)", val, ok)
	}

	rb.Push(30)
	s := rb.Slice()
	if len(s) != 3 || s[0] != 10 || s[1] != 20 || s[2] != 30 {
		t.Fatalf("expected [10, 20, 30], got %v", s)
	}

	// Overwrite: 40 should drop 10
	rb.Push(40)
	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}
	s = rb.Slice()
	if len(s) != 3 || s[0] != 20 || s[1] != 30 || s[2] != 40 {
		t.Fatalf("expected [20, 30, 40], got %v", s)
	}
	val, ok = rb.Latest()
	if !ok || val != 40 {
		t.Fatalf("expected latest 40, got %v (%v)", val, ok)
	}

	// Overwrite again: 50 should drop 20
	rb.Push(50)
	s = rb.Slice()
	if len(s) != 3 || s[0] != 30 || s[1] != 40 || s[2] != 50 {
		t.Fatalf("expected [30, 40, 50], got %v", s)
	}

	rb.Clear()
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after Clear(), got %d", rb.Len())
	}
}

func TestRingBuffer_Concurrent(t *testing.T) {
	rb := NewRingBuffer[int](50)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rb.Push(workerID*1000 + j)
				_ = rb.Slice()
				_, _ = rb.Latest()
				_ = rb.Len()
			}
		}(i)
	}
	wg.Wait()
	if rb.Len() != 50 {
		t.Fatalf("expected len 50, got %d", rb.Len())
	}
}
