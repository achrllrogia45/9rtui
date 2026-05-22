package repository

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type testEntity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

func (e *testEntity) GetID() string {
	if e == nil {
		return ""
	}
	return e.ID
}
func (e *testEntity) SetID(id string) { e.ID = id }

func TestJSONRepositoryCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.json")
	repo, err := NewJSONRepository[*testEntity](path, WithClock(func() time.Time {
		return time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatalf("NewJSONRepository() error = %v", err)
	}

	created, err := repo.Create(&testEntity{ID: " b ", Name: "beta"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != "b" {
		t.Fatalf("Create() ID = %q", created.ID)
	}
	if _, err := repo.Create(&testEntity{ID: "b"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create() duplicate error = %v", err)
	}

	if _, err := repo.Upsert(&testEntity{ID: "a", Name: "alpha"}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	items, err := repo.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	ids := []string{items[0].ID, items[1].ID}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("List() ids = %#v", ids)
	}

	got, err := repo.Get("a")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Name != "alpha" {
		t.Fatalf("Get() Name = %q", got.Name)
	}

	updated, err := repo.Update("a", func(e *testEntity) (*testEntity, error) {
		e.ID = "evil"
		e.Name = "ALPHA"
		return e, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ID != "a" || updated.Name != "ALPHA" {
		t.Fatalf("Update() = %#v", updated)
	}

	if err := repo.Delete("b"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.Get("b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() missing error = %v", err)
	}
}

func TestJSONRepositoryReplaceAllValidation(t *testing.T) {
	repo, err := NewJSONRepository[*testEntity](filepath.Join(t.TempDir(), "items.json"))
	if err != nil {
		t.Fatalf("NewJSONRepository() error = %v", err)
	}

	if err := repo.ReplaceAll([]*testEntity{{ID: "x"}, {ID: "x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ReplaceAll() duplicate error = %v", err)
	}
	if err := repo.ReplaceAll([]*testEntity{{ID: ""}}); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("ReplaceAll() empty id error = %v", err)
	}
}
