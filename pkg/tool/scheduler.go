package tool

import (
	"context"
	"errors"
	"sync"
)

var errSiblingCapabilityFailed = errors.New("sibling capability failed")

const siblingErrorContent = "cancelled because a sibling capability failed"

// Scheduler overlaps only contiguous concurrency-safe calls. Unsafe calls are
// singleton barriers. Safe-group results retain observable completion order;
// barriers preserve order between groups.
type Scheduler struct {
	executor      *Executor
	registry      *Registry
	maxConcurrent int
}

func NewScheduler(executor *Executor, registry *Registry, maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultConcurrency
	}
	return &Scheduler{executor: executor, registry: registry, maxConcurrent: maxConcurrent}
}

func (s *Scheduler) Execute(ctx context.Context, requests []Request) []Result {
	results := make([]Result, 0, len(requests))
	for start := 0; start < len(requests); {
		safe := s.safe(requests[start])
		end := start + 1
		if safe {
			for end < len(requests) && s.safe(requests[end]) {
				end++
			}
		}
		if !safe {
			results = append(results, s.executor.Execute(ctx, requests[start]))
			start = end
			continue
		}
		groupCtx, cancel := context.WithCancelCause(ctx)
		var wg sync.WaitGroup
		workers := s.maxConcurrent
		if groupSize := end - start; workers > groupSize {
			workers = groupSize
		}
		jobs := make(chan int)
		// A group-sized buffer lets a worker publish its terminal result before
		// looking for more work. This both records the actual completion order
		// and prevents the dispatcher from forming a cycle with result delivery.
		completed := make(chan Result, end-start)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					result := s.executor.Execute(groupCtx, requests[index])
					completed <- result
					if result.Name == "Bash" && result.IsError && result.Code == "execution_failed" {
						cancel(errSiblingCapabilityFailed)
					}
					if groupCtx.Err() != nil {
						return
					}
				}
			}()
		}
		next := start
	dispatch:
		for ; next < end; next++ {
			select {
			case jobs <- next:
			case <-groupCtx.Done():
				break dispatch
			}
		}
		// Route never-dispatched requests through Executor while the group
		// context is canceled. This records their terminal sibling result in the
		// exactly-once ledger and applies the same credential boundary as every
		// dispatched call, without running hooks or the implementation.
		for ; next < end; next++ {
			completed <- s.executor.Execute(groupCtx, requests[next])
		}
		close(jobs)
		wg.Wait()
		close(completed)
		for result := range completed {
			results = append(results, result)
		}
		cancel(context.Canceled)
		start = end
	}
	return results
}

func (s *Scheduler) safe(request Request) (safe bool) {
	defer func() {
		if recover() != nil {
			// Preflight is an optimization only. Route panicking validation or
			// classification through Executor so its common failure boundary
			// produces the request's terminal result.
			safe = false
		}
	}()
	descriptor, ok := s.registry.Resolve(request.Name)
	if !ok {
		return false
	}
	// Validation belongs to an untrusted descriptor. Give preflight its own
	// bytes so a mutating implementation cannot rewrite the accepted request
	// before Executor captures the original input and permission evidence.
	input, err := descriptor.Validate(cloneRaw(request.Input))
	if err != nil {
		return false
	}
	return descriptor.classification(input).ConcurrencySafe
}

func siblingErrorResult(toolUseID, name string) Result {
	return Result{
		ToolUseID: toolUseID,
		Name:      name,
		IsError:   true,
		Code:      "sibling_error",
		Content:   siblingErrorContent,
	}
}
