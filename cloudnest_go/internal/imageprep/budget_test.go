package imageprep

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestThumbnailBudgetBoundsActiveMemoryAndWaiters(t *testing.T) {
	b := NewBudget()
	ctx := context.Background()
	release, err := b.Acquire(ctx, MemoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := b.Acquire(waiting, 1)
			if r != nil {
				r()
			}
			errs <- e
		}()
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		b.mu.Lock()
		count := b.waiting
		b.mu.Unlock()
		if count == 16 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			release()
			t.Fatal("waiters did not register")
		}
		time.Sleep(time.Millisecond)
	}
	if r, err := b.Acquire(ctx, 1); !errors.Is(err, ErrBusy) || r != nil {
		t.Fatal("unbounded queue", err)
	}
	cancel()
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Fatal("waiter did not cancel", err)
		}
	}
	release()
	release() // Idempotent: a double defer must not underflow accounting.
	first, err := b.Acquire(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := b.Acquire(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	expired, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if r, err := b.Acquire(expired, 1); !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatal("active count not bounded", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 2 || b.used != 2 || b.waiting != 0 {
		t.Fatal(b.active, b.used, b.waiting)
	}
}

func TestThumbnailBudgetReleasesAndTimesOut(t *testing.T) {
	b := NewBudget()
	release, err := b.Acquire(context.Background(), MemoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		r, err := b.Acquire(context.Background(), MemoryBudget)
		if r != nil {
			r()
		}
		result <- err
	}()
	release()
	if err := <-result; err != nil {
		t.Fatal("waiter did not resume", err)
	}
	release, err = b.Acquire(context.Background(), MemoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	if r, err := b.Acquire(context.Background(), 1); !errors.Is(err, ErrBusy) || r != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < WaitTimeout || elapsed > 3*time.Second {
		t.Fatal("unbounded wait", elapsed)
	}
}
