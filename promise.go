package promise

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Promise[T any] struct {
	f func (ctx context.Context) (*T, error)
	value *T
	err error
	resolved bool
	rejected bool
	cancel context.CancelFunc
	callbacks []func(ctx context.Context, value *T, err error)
	mu *sync.Mutex
}

func New[T any](ctx context.Context, f func (ctx context.Context) (*T, error)) *Promise[T] {
	p := &Promise[T]{
		mu: &sync.Mutex{},
		f: f,
	}

	launch(ctx, p)
	return p
}

func launch[T any](ctx context.Context, p *Promise[T]) {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	go func(ctx context.Context) {
		doneChan := make(chan *T)
		errChan := make(chan error)

		go func(ctx context.Context) {
			result, err := p.f(ctx)
			if err != nil {
				errChan <- err
				return
			}
			doneChan <- result
		}(ctx)

		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				p.err = err
			} else {
				p.err = fmt.Errorf("context cancelled")
			}
			p.rejected = true
		case err := <- errChan:
			p.err = err
			p.rejected = true
		case result := <- doneChan:
			p.value = result
			p.resolved = true
		}

		p.executeCallbacks(ctx)
	}(ctx)
}

func (p *Promise[T]) executeCallbacks(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, cb := range p.callbacks {
		cb(ctx, p.value, p.err)
	}
}

func (p *Promise[T]) settled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolved || p.rejected
}

func (p *Promise[T]) Await() (*T, error) {

	p.mu.Lock()
	doneChan := make(chan *T)
	errChan := make(chan error)

	p.callbacks = append(p.callbacks, func(ctx context.Context, value *T, err error) {
		if err != nil {
			errChan <- err
			return
		}
		doneChan <- value
	})
	p.mu.Unlock()

	select {
	case result := <- doneChan:
		return result, nil
	case err := <- errChan:
		return nil, err
	}
}

func Then[T, V any](promise *Promise[T], f func (ctx context.Context, value *T, err error) (*V, error)) *Promise[V] {
	next := &Promise[V]{
		mu: &sync.Mutex{},
	}

	promise.mu.Lock()
	promise.callbacks = append(promise.callbacks, func(ctx context.Context, value *T, err error) {
		next.f = func(ctx context.Context) (*V, error) {
			if err != nil {
				return f(ctx, nil, err)
			}
			return f(ctx, value, nil)
		}
		launch(ctx, next)
	})
	promise.mu.Unlock()

	return next
}

type promiseComplete[T any] struct {
	value T
	index int
}

func All[T any](promises... *Promise[T]) ([]T, error) {
	var results []promiseComplete[T]

	errChan := make(chan error)
	doneChan := make(chan promiseComplete[T])

	for index, promise := range promises {
		go func(i int, p *Promise[T]) {
			value, err := p.Await()
			if err != nil {
				errChan <- err
				return
			}
			doneChan <- promiseComplete[T]{
				value: *value,
				index: i,
			}
		}(index, promise)
	}

	for len(results) != len(promises) {
		select {
		case err := <- errChan:
			return nil, err
		case result := <- doneChan:
			results = append(results, result)
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	return collect[promiseComplete[T], T](results, func(result promiseComplete[T]) T {
		return result.value
	}), nil
}

func collect[T, U any](in []T, f func (t T) U) (out []U) {
	for _, t := range in {
		out = append(out, f(t))
	}
	return
}