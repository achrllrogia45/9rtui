package repository

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Account struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Username  string            `json:"username,omitempty"`
	SecretRef string            `json:"secret_ref,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (a *Account) GetID() string {
	if a == nil {
		return ""
	}
	return a.ID
}

func (a *Account) SetID(id string) {
	if a != nil {
		a.ID = id
	}
}

type AccountRepository struct {
	*JSONRepository[*Account]
}

func NewAccountRepository(path string, opts ...Option) (*AccountRepository, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultAccountRepositoryPath()
	}
	repo, err := NewJSONRepository[*Account](path, opts...)
	if err != nil {
		return nil, err
	}
	return &AccountRepository{JSONRepository: repo}, nil
}

func DefaultAccountRepositoryPath() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "9rtui", "accounts.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "9rtui", "accounts.json")
	}
	return filepath.Join(".", "accounts.json")
}

func (r *AccountRepository) Save(account *Account) (*Account, error) {
	now := time.Now().UTC()
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	return r.Upsert(account)
}
