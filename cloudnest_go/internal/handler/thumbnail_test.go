package handler

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/config"
)

func TestThumbnailDimensionAdmission(t *testing.T) {
	for _, dimensions := range [][2]int{{0, 1}, {1, -1}, {16385, 1}, {1, 16385}, {6000, 4001}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
		if _, err := thumbnailWeight(dimensions[0], dimensions[1]); err == nil {
			t.Fatal("unsafe dimensions accepted", dimensions)
		}
	}
	for _, dimensions := range [][2]int{{6000, 4000}, {16384, 1}, {1, 16384}, {1, 1}} {
		if weight, err := thumbnailWeight(dimensions[0], dimensions[1]); err != nil || weight > thumbnailMemoryBudget {
			t.Fatal(dimensions, weight, err)
		}
	}
}

func TestThumbnailBudgetBoundsActiveMemoryAndWaiters(t *testing.T) {
	b := newThumbnailBudget()
	ctx := context.Background()
	release, err := b.acquire(ctx, thumbnailMemoryBudget)
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
			r, e := b.acquire(waiting, 1)
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
	if r, err := b.acquire(ctx, 1); !errors.Is(err, errThumbnailBusy) || r != nil {
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
	first, err := b.acquire(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := b.acquire(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	expired, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if r, err := b.acquire(expired, 1); !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatal("active count not bounded", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != 2 || b.used != 2 || b.waiting != 0 {
		t.Fatal(b.active, b.used, b.waiting)
	}
}

func TestThumbnailBudgetReleasesAndTimesOut(t *testing.T) {
	b := newThumbnailBudget()
	release, err := b.acquire(context.Background(), thumbnailMemoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		r, err := b.acquire(context.Background(), thumbnailMemoryBudget)
		if r != nil {
			r()
		}
		result <- err
	}()
	release()
	if err := <-result; err != nil {
		t.Fatal("waiter did not resume", err)
	}
	release, err = b.acquire(context.Background(), thumbnailMemoryBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	if r, err := b.acquire(context.Background(), 1); !errors.Is(err, errThumbnailBusy) || r != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < thumbnailWaitTimeout || elapsed > 3*time.Second {
		t.Fatal("unbounded wait", elapsed)
	}
}

func thumbnailTestFile(t *testing.T, content []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func TestThumbnailRejectsEncodedAndHeaderLimits(t *testing.T) {
	source := thumbnailTestFile(t, []byte("small"))
	if err := source.Truncate(thumbnailMaxEncodedBytes + 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := thumbnailConfig(context.Background(), source); !errors.Is(err, errThumbnailLimit) {
		t.Fatal("encoded limit", err)
	}
	// Many legal JPEG APP1 segments before SOF: config parsing must be bounded.
	data := []byte{0xff, 0xd8}
	segment := append([]byte{0xff, 0xe1, 0xff, 0xff}, make([]byte, 65533)...)
	for len(data) <= int(thumbnailMaxHeaderBytes) {
		data = append(data, segment...)
	}
	source = thumbnailTestFile(t, data)
	if _, _, err := thumbnailConfig(context.Background(), source); !errors.Is(err, errThumbnailLimit) {
		t.Fatal("header limit", err)
	}
}
func TestThumbnailCancellationLeavesNoCacheOrDetachedWork(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 128, 128)), nil); err != nil {
		t.Fatal(err)
	}
	source := thumbnailTestFile(t, encoded.Bytes())
	root := t.TempDir()
	handler := FileHandler{Cfg: config.Config{DataDir: root}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler.buildOrGetThumbnail(ctx, source, 1, 1, "test", 64); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled build wrote cache", entries, err)
	}
	if _, err := resizeThumbnail(ctx, image.NewRGBA(image.Rect(0, 0, 1024, 1024)), 64); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// No separate decoding goroutine: cancellation errors propagate through the
// decoder's real reader, including when cancellation happens after preflight.
func TestThumbnailDecodeRejectsCancellationAndChangedGeometry(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	img.Set(0, 0, color.White)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	source := thumbnailTestFile(t, encoded.Bytes())
	cfg, format, err := thumbnailConfig(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeThumbnail(ctx, source, cfg, format); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cfg.Width = 1
	if _, err := decodeThumbnail(context.Background(), source, cfg, format); !errors.Is(err, errThumbnailInvalid) {
		t.Fatal("geometry mismatch", err)
	}
}
