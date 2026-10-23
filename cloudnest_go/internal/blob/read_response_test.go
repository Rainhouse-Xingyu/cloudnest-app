package blob

import (
	"context"
	"testing"

	"cloudenest_go/model"
)

func TestMetadataResponseCancellationCannotBypassFailedPin(t *testing.T) {
	manager := NewManager(nil)
	parent, cancel := context.WithCancel(context.Background())
	ctx, scope := manager.Begin(parent)
	defer scope.Close()
	cancel()
	if err := scope.CheckResponse(); err != nil {
		t.Fatal("metadata-only response was gated", err)
	}
	if _, err := Pin(ctx, model.Storage{ID: 1}); err == nil {
		t.Fatal("canceled physical reader accepted")
	}
	if err := scope.CheckResponse(); err == nil {
		t.Fatal("failed physical pin became metadata-only response")
	}
}
