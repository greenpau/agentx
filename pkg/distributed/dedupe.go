package distributed

import (
	"errors"
	"sync"
)

// Deduper is a bounded process-local FIFO membership ring. Eviction means
// absence no longer proves novelty; it is never durable acknowledgement.
type Deduper struct {
	mu       sync.Mutex
	capacity int
	queue    []MessageID
	seen     map[MessageID]struct{}
}

func NewDeduper(capacity int) (*Deduper, error) {
	if capacity <= 0 {
		return nil, errors.New("deduplication capacity must be positive")
	}
	return &Deduper{capacity: capacity, seen: make(map[MessageID]struct{}, capacity)}, nil
}

// SeenOrAdd reports an in-window duplicate without refreshing its age.
func (d *Deduper) SeenOrAdd(id MessageID) (bool, error) {
	if err := ValidateOpaqueID("message ID", string(id)); err != nil {
		return false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.seen[id]; exists {
		return true, nil
	}
	d.seen[id] = struct{}{}
	d.queue = append(d.queue, id)
	if len(d.queue) > d.capacity {
		oldest := d.queue[0]
		d.queue = d.queue[1:]
		delete(d.seen, oldest)
	}
	return false, nil
}

func (d *Deduper) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue)
}
