package sampler

import (
	"context"
	"sync"
)

func (s *Sampler) each(ctx context.Context, n int, fn func(context.Context, int)) {
	workers := s.opts.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}

	queue := make(chan int, n)
	for i := 0; i < n; i++ {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for i := range queue {
				fn(ctx, i)
			}
		}()
	}
	wg.Wait()
}
