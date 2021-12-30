package promise

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Promise[T any] struct {
	f         func(ctx context.Context) (*T, error)
	value     *T
	err       error
	resolved  bool
	rejected  bool
	cancel    context.CancelFunc
	ctx       context.Context
	callbacks []func(ctx context.Context, value *T, err error)
	mu        *sync.Mutex
}

func New[T any](ctx context.Context, f func(ctx context.Context) (*T, error)) *Promise[T] {
	ctx, cancel := context.WithCancel(ctx)
	p := &Promise[T]{
		mu: &sync.Mutex{},
		f:  f,
		ctx: ctx,
		cancel: cancel,
	}

	launch(p)
	return p
}

func Resolve[T any](value T) *Promise[T] {
	ctx, cancel := context.WithCancel(context.Background())
	return &Promise[T]{
		resolved: true,
		value: &value,
		mu: &sync.Mutex{},
		ctx: ctx,
		cancel: cancel,
	}
}

func Reject[T any](err error) *Promise[T] {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return &Promise[T]{
		rejected: true,
		err: err,
		mu: &sync.Mutex{},
		ctx: ctx,
		cancel: cancel,
	}
}

func launch[T any](p *Promise[T]) {
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
			p.mu.Lock()
			if err := ctx.Err(); err != nil {
				p.err = err
			} else {
				p.err = fmt.Errorf("context canceled")
			}
			p.rejected = true
			p.mu.Unlock()
		case err := <-errChan:
			p.mu.Lock()
			p.err = err
			p.rejected = true
			p.mu.Unlock()
		case result := <-doneChan:
			p.mu.Lock()
			p.value = result
			p.resolved = true
			p.mu.Unlock()
		}

		p.executeCallbacks(ctx)
	}(p.ctx)
}

func (p *Promise[T]) executeCallbacks(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, cb := range p.callbacks {
		go cb(ctx, p.value, p.err)
	}
}

func (p *Promise[T]) Await() (*T, error) {
	p.mu.Lock()
	if p.resolved || p.rejected {
		defer p.mu.Unlock()
		return p.value, p.err
	}

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
	case result := <-doneChan:
		return result, nil
	case err := <-errChan:
		return nil, err
	}
}

func Then[T, V any](promise *Promise[T], f func(ctx context.Context, value *T, err error) (*V, error)) *Promise[V] {
	return New[V](promise.ctx, func(ctx context.Context) (*V, error) {
		value, err := promise.Await()
		return f(ctx, value, err)
	})
}

type promiseComplete[T any] struct {
	value T
	index int
}

func All[T any](ctx context.Context, promises ...*Promise[T]) *Promise[[]T] {
	return New(ctx, func(ctx context.Context) (*[]T, error) {
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
			case err := <-errChan:
				return nil, err
			case result := <-doneChan:
				results = append(results, result)
			case <- ctx.Done():
				if err := ctx.Err(); err != nil {
					return nil, err
				} else {
					return nil, fmt.Errorf("context canceled")
				}
			}
		}

		sort.SliceStable(results, func(i, j int) bool {
			return results[i].index < results[j].index
		})

		mapped := mapWith[promiseComplete[T], T](results, func(result promiseComplete[T]) T {
			return result.value
		})

		return &mapped, nil
	})
}

func Race[T any](ctx context.Context, promises ...*Promise[T]) *Promise[T] {
	return New(ctx, func(ctx context.Context) (*T, error) {
		errChan := make(chan error)
		doneChan := make(chan *T)

		defer func() {
			for _, promise := range promises {
				promise.mu.Lock()
				if promise.cancel != nil {
					promise.cancel()
				}
				promise.mu.Unlock()
			}
		}()

		for _, promise := range promises {
			go func (p *Promise[T]) {
				value, err := p.Await()
				if err != nil {
					errChan <- err
					return
				}
				doneChan <- value
			}(promise)
		}

		select {
		case err := <-errChan:
			return nil, err
		case result := <-doneChan:
			return result, nil
		case <- ctx.Done():
			if err := ctx.Err(); err != nil {
				return nil, err
			} else {
				return nil, fmt.Errorf("context canceled")
			}
		}
	})
}

func mapWith[T, U any](in []T, f func(t T) U) (out []U) {
	for _, t := range in {
		out = append(out, f(t))
	}
	return
}
