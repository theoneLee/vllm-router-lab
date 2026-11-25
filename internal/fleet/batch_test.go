package fleet

import (
	"context"
	"testing"
	"time"
)

func TestBatchCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBatcher(ctx, time.Millisecond, 2)
	dead, stop := context.WithCancel(ctx)
	stop()
	if b.step(dead, 100) || !b.step(ctx, 5) {
		t.Fatal("step or cancellation failed")
	}
}
