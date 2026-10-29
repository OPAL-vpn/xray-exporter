package sampler

import (
	"context"
	"sync"
)

// Each runs fn for 0..n-1 on at most workers goroutines and returns when every
// started call has finished. A cancelled ctx stops it from taking more items.
func Each(ctx context.Context, workers, n int, fn func(context.Context, int)) {
	if n == 0 {
		return
	}
	workers = max(1, min(workers, n))

	queue := make(chan int, n)
	for i := 0; i < n; i++ {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					return
				}
				fn(ctx, i)
			}
		}()
	}
	wg.Wait()
}

func (s *Sampler) each(ctx context.Context, n int, fn func(context.Context, int)) {
	Each(ctx, s.opts.Workers, n, fn)
}
