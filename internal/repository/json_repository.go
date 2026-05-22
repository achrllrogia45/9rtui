package repository

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("repository: not found")
	ErrConflict = errors.New("repository: conflict")
	ErrEmptyID  = errors.New("repository: empty id")
)

type Entity interface {
	GetID() string
	SetID(string)
}

type Clock func() time.Time

type JSONRepository[T Entity] struct {
	path  string
	clock Clock
	mu    sync.RWMutex
}

type Option func(*options)

type options struct{ clock Clock }

func WithClock(clock Clock) Option {
	return func(o *options) {
		if clock != nil {
			o.clock = clock
		}
	}
}

func NewJSONRepository[T Entity](path string, opts ...Option) (*JSONRepository[T], error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("repository: path required")
	}
	cfg := options{clock: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	repo := &JSONRepository[T]{path: path, clock: cfg.clock}
	if err := repo.ensure(); err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *JSONRepository[T]) Path() string { return r.path }

func (r *JSONRepository[T]) List() ([]T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	store, err := r.readStore()
	if err != nil {
		return nil, err
	}
	items := make([]T, 0, len(store.Items))
	for _, item := range store.Items {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].GetID() < items[j].GetID() })
	return items, nil
}

func (r *JSONRepository[T]) Get(id string) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var zero T
	id = strings.TrimSpace(id)
	if id == "" {
		return zero, ErrEmptyID
	}
	store, err := r.readStore()
	if err != nil {
		return zero, err
	}
	item, ok := store.Items[id]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return item, nil
}

func (r *JSONRepository[T]) Exists(id string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return false, ErrEmptyID
	}
	store, err := r.readStore()
	if err != nil {
		return false, err
	}
	_, ok := store.Items[id]
	return ok, nil
}

func (r *JSONRepository[T]) Create(item T) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := strings.TrimSpace(item.GetID())
	if id == "" {
		return item, ErrEmptyID
	}
	item.SetID(id)
	store, err := r.readStore()
	if err != nil {
		return item, err
	}
	if _, exists := store.Items[id]; exists {
		return item, fmt.Errorf("%w: %s", ErrConflict, id)
	}
	store.Items[id] = item
	store.UpdatedAt = r.clock().UTC()
	return item, r.writeStore(store)
}

func (r *JSONRepository[T]) Upsert(item T) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := strings.TrimSpace(item.GetID())
	if id == "" {
		return item, ErrEmptyID
	}
	item.SetID(id)
	store, err := r.readStore()
	if err != nil {
		return item, err
	}
	store.Items[id] = item
	store.UpdatedAt = r.clock().UTC()
	return item, r.writeStore(store)
}

func (r *JSONRepository[T]) Update(id string, mutate func(T) (T, error)) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero T
	id = strings.TrimSpace(id)
	if id == "" {
		return zero, ErrEmptyID
	}
	if mutate == nil {
		return zero, errors.New("repository: mutate required")
	}
	store, err := r.readStore()
	if err != nil {
		return zero, err
	}
	current, ok := store.Items[id]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	next, err := mutate(current)
	if err != nil {
		return zero, err
	}
	next.SetID(id)
	store.Items[id] = next
	store.UpdatedAt = r.clock().UTC()
	return next, r.writeStore(store)
}

func (r *JSONRepository[T]) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyID
	}
	store, err := r.readStore()
	if err != nil {
		return err
	}
	if _, ok := store.Items[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(store.Items, id)
	store.UpdatedAt = r.clock().UTC()
	return r.writeStore(store)
}

func (r *JSONRepository[T]) ReplaceAll(items []T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	store := jsonStore[T]{Version: 1, Items: make(map[string]T, len(items)), UpdatedAt: r.clock().UTC()}
	for _, item := range items {
		id := strings.TrimSpace(item.GetID())
		if id == "" {
			return ErrEmptyID
		}
		item.SetID(id)
		if _, exists := store.Items[id]; exists {
			return fmt.Errorf("%w: duplicate id %s", ErrConflict, id)
		}
		store.Items[id] = item
	}
	return r.writeStore(store)
}

func (r *JSONRepository[T]) ensure() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("repository: create dir: %w", err)
	}
	if _, err := os.Stat(r.path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("repository: stat: %w", err)
	}
	store := jsonStore[T]{Version: 1, Items: map[string]T{}, UpdatedAt: r.clock().UTC()}
	return r.writeStore(store)
}

func (r *JSONRepository[T]) readStore() (jsonStore[T], error) {
	var store jsonStore[T]
	b, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsonStore[T]{Version: 1, Items: map[string]T{}, UpdatedAt: r.clock().UTC()}, nil
		}
		return store, fmt.Errorf("repository: read: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return jsonStore[T]{Version: 1, Items: map[string]T{}, UpdatedAt: r.clock().UTC()}, nil
	}
	if err := json.Unmarshal(b, &store); err != nil {
		return store, fmt.Errorf("repository: decode: %w", err)
	}
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Items == nil {
		store.Items = map[string]T{}
	}
	return store, nil
}

func (r *JSONRepository[T]) writeStore(store jsonStore[T]) error {
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Items == nil {
		store.Items = map[string]T{}
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("repository: create dir: %w", err)
	}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("repository: encode: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), "."+filepath.Base(r.path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("repository: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("repository: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("repository: chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("repository: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("repository: close temp: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("repository: rename: %w", err)
	}
	return syncDir(filepath.Dir(r.path))
}

type jsonStore[T Entity] struct {
	Version   int          `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
	Items     map[string]T `json:"items"`
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("repository: open dir: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("repository: sync dir: %w", err)
	}
	return nil
}
