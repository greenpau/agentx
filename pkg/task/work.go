package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CreateWork creates a durable pending planning task.
func (m *Manager) CreateWork(subject, description, activeForm string, metadata map[string]string) (result WorkItem, resultErr error) {
	if m.hostCallbackBusy() {
		return WorkItem{}, ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(description) == "" || strings.TrimSpace(activeForm) == "" {
		return WorkItem{}, errors.New("subject, description, and active form are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return WorkItem{}, ErrClosed
	}
	if len(m.work) >= maximumWorkRecords {
		return WorkItem{}, fmt.Errorf("work item count cannot exceed %d", maximumWorkRecords)
	}
	id, err := m.nextIDLocked('t')
	if err != nil {
		return WorkItem{}, err
	}
	now := m.currentTime().UTC()
	item := newWorkItem(id, subject, description, activeForm, metadata, now)
	prospective := cloneWorkMap(m.work)
	prospective[id] = item
	normalized, err := m.validateState(m.tasks, prospective, m.todos)
	if err != nil {
		return WorkItem{}, err
	}
	previous := m.work
	m.work = normalized
	if err := m.persistLocked(); err != nil {
		m.work = previous
		return WorkItem{}, err
	}
	return cloneWork(m.work[id]), nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

// GetWork returns one work item.
func (m *Manager) GetWork(id ID) (result WorkItem, resultErr error) {
	if m.hostCallbackBusy() {
		return WorkItem{}, ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.work[id]
	if !ok {
		return WorkItem{}, ErrNotFound
	}
	return cloneWork(item), nil
}

// ListWork returns deterministic creation-order summaries.
func (m *Manager) ListWork() []WorkItem {
	if m.hostCallbackBusy() {
		return nil
	}
	m.mu.RLock()
	items := cloneWorkItems(m.work)
	m.mu.RUnlock()
	sortWorkItems(items)
	return items
}

// ListWorkContext returns deterministic creation-order summaries through an
// error-bearing boundary suitable for tool adapters. Unlike the legacy
// ListWork snapshot, it can distinguish a host-callback recursion claim and
// abandon lock acquisition when its context is cancelled.
func (m *Manager) ListWorkContext(ctx context.Context) (items []WorkItem, resultErr error) {
	if m.hostCallbackBusy() {
		return nil, ErrBusy
	}
	defer func() {
		if resultErr != ErrBusy {
			resultErr = m.sanitizePublicError(resultErr)
		}
	}()
	if ctx == nil {
		return nil, errors.New("task list-work context is nil")
	}
	if err := m.acquireReadLock(ctx); err != nil {
		return nil, err
	}
	items = cloneWorkItems(m.work)
	m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortWorkItems(items)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func cloneWorkItems(source map[ID]WorkItem) []WorkItem {
	items := make([]WorkItem, 0, len(source))
	for _, item := range source {
		items = append(items, cloneWork(item))
	}
	return items
}

func sortWorkItems(items []WorkItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
}

// UpdateWork applies a patch atomically, validates relationships, and rejects
// cycles before publishing any state.
func (m *Manager) UpdateWork(id ID, patch WorkPatch) (result WorkItem, resultErr error) {
	if m.hostCallbackBusy() {
		return WorkItem{}, ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return WorkItem{}, ErrClosed
	}
	item, ok := m.work[id]
	if !ok {
		return WorkItem{}, ErrNotFound
	}
	if patch.Delete {
		for otherID, other := range m.work {
			if otherID == id {
				continue
			}
			if containsID(other.Blockers, id) {
				return WorkItem{}, fmt.Errorf("cannot delete task referenced by %s", otherID)
			}
		}
		prospective := cloneWorkMap(m.work)
		delete(prospective, id)
		normalized, validateErr := m.validateState(m.tasks, prospective, m.todos)
		if validateErr != nil {
			return WorkItem{}, validateErr
		}
		previous := m.work
		m.work = normalized
		if err := m.persistLocked(); err != nil {
			m.work = previous
			return WorkItem{}, err
		}
		return cloneWork(item), nil
	}
	updated := cloneWork(item)
	if patch.Subject != nil {
		if strings.TrimSpace(*patch.Subject) == "" {
			return WorkItem{}, errors.New("subject cannot be empty")
		}
		updated.Subject = *patch.Subject
	}
	if patch.Description != nil {
		if strings.TrimSpace(*patch.Description) == "" {
			return WorkItem{}, errors.New("description cannot be empty")
		}
		updated.Description = *patch.Description
	}
	if patch.ActiveForm != nil {
		if strings.TrimSpace(*patch.ActiveForm) == "" {
			return WorkItem{}, errors.New("active form cannot be empty")
		}
		updated.ActiveForm = *patch.ActiveForm
	}
	if patch.Owner != nil {
		updated.Owner = *patch.Owner
	}
	if patch.Status != nil {
		if !validWorkStatus(*patch.Status) {
			return WorkItem{}, errors.New("invalid work status")
		}
		updated.Status = *patch.Status
	}
	if patch.Blockers != nil {
		if len(*patch.Blockers) > maximumWorkRecords {
			return WorkItem{}, fmt.Errorf("blocker count cannot exceed %d", maximumWorkRecords)
		}
		seen := make(map[ID]struct{}, len(*patch.Blockers))
		for _, blocker := range *patch.Blockers {
			if blocker == id {
				return WorkItem{}, errors.New("task cannot block itself")
			}
			if _, exists := m.work[blocker]; !exists {
				return WorkItem{}, errors.New("unknown blocker")
			}
			if _, duplicate := seen[blocker]; duplicate {
				return WorkItem{}, errors.New("duplicate blocker")
			}
			seen[blocker] = struct{}{}
		}
		updated.Blockers = append([]ID(nil), (*patch.Blockers)...)
	}
	if updated.Metadata == nil && patch.Metadata != nil {
		updated.Metadata = make(map[string]string)
	}
	for key, value := range patch.Metadata {
		if value == nil {
			delete(updated.Metadata, key)
		} else {
			updated.Metadata[key] = *value
		}
	}
	prospective := cloneWorkMap(m.work)
	prospective[id] = updated
	updated.UpdatedAt = m.currentTime().UTC()
	prospective[id] = updated
	normalized, err := m.validateState(m.tasks, prospective, m.todos)
	if err != nil {
		return WorkItem{}, err
	}
	previous := m.work
	m.work = normalized
	if err := m.persistLocked(); err != nil {
		m.work = previous
		return WorkItem{}, err
	}
	return cloneWork(m.work[id]), nil
}

func cloneWorkMap(input map[ID]WorkItem) map[ID]WorkItem {
	result := make(map[ID]WorkItem, len(input))
	for id, item := range input {
		result[id] = cloneWork(item)
	}
	return result
}

func validWorkStatus(status WorkStatus) bool {
	return status == WorkPending || status == WorkInProgress || status == WorkCompleted
}

func containsID(ids []ID, target ID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func dependencyCycle(items map[ID]WorkItem) bool {
	const (
		unseen = iota
		visiting
		visited
	)
	state := make(map[ID]int, len(items))
	var visit func(ID) bool
	visit = func(id ID) bool {
		switch state[id] {
		case visiting:
			return true
		case visited:
			return false
		}
		state[id] = visiting
		for _, blocker := range items[id].Blockers {
			if visit(blocker) {
				return true
			}
		}
		state[id] = visited
		return false
	}
	for id := range items {
		if visit(id) {
			return true
		}
	}
	return false
}

// ReplaceTodos atomically replaces the legacy todo surface. Completed-only
// lists persist as empty so stale completed work does not reappear.
func (m *Manager) ReplaceTodos(todos []Todo) (resultErr error) {
	if m.hostCallbackBusy() {
		return ErrBusy
	}
	defer func() {
		resultErr = m.sanitizePublicError(resultErr)
	}()
	if err := validateTodos(todos); err != nil {
		return err
	}
	allComplete := len(todos) > 0
	for _, todo := range todos {
		if todo.Status != WorkCompleted {
			allComplete = false
			break
		}
	}
	if allComplete {
		todos = nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	previous := m.todos
	m.todos = append([]Todo(nil), todos...)
	if _, err := m.validateState(m.tasks, m.work, m.todos); err != nil {
		m.todos = previous
		return err
	}
	if err := m.persistLocked(); err != nil {
		m.todos = previous
		return err
	}
	return nil
}

// Todos returns an immutable legacy todo snapshot.
func (m *Manager) Todos() []Todo {
	if m.hostCallbackBusy() {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Todo(nil), m.todos...)
}
