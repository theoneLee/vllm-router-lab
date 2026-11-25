package fleet

import (
	"context"
	"time"
)

// Each generation submits at most one decode token per iteration. Prefill
// competes for the same budget and carries unfinished chunks to the next step.
type demand struct {
	ctx  context.Context
	left int
	done chan struct{}
}
type batcher struct{ incoming chan *demand }

func newBatcher(ctx context.Context, tick time.Duration, budget int) *batcher {
	if tick == 0 {
		return nil
	}
	b := &batcher{make(chan *demand)}
	go func() {
		var queue []*demand
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case d := <-b.incoming:
				queue = append(queue, d)
			case <-t.C:
				available := budget
				rest := queue[:0]
				for _, d := range queue {
					if d.ctx.Err() != nil {
						continue
					}
					take := min(d.left, available)
					available -= take
					d.left -= take
					if d.left == 0 {
						close(d.done)
					} else {
						rest = append(rest, d)
					}
				}
				clear(queue[len(rest):])
				queue = rest
			}
		}
	}()
	return b
}
func (b *batcher) step(ctx context.Context, n int) bool {
	if b == nil || n == 0 {
		return ctx.Err() == nil
	}
	d := &demand{ctx, n, make(chan struct{})}
	select {
	case b.incoming <- d:
	case <-ctx.Done():
		return false
	}
	select {
	case <-d.done:
		return true
	case <-ctx.Done():
		return false
	}
}
