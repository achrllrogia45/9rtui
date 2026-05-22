package domain

type Account struct {
	ID        string
	Provider  string
	AuthType  string
	Name      string
	Email     string
	Priority  int
	IsActive  bool
	CreatedAt string
	UpdatedAt string
}

type AccountExport struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	AuthType  string `json:"authType"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Priority  int    `json:"priority"`
	IsActive  bool   `json:"isActive"`
	Data      any    `json:"data"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type BackupInfo struct {
	Path          string
	Modified      string
	Size          int64
	Accounts      int
	ProviderCount map[string]int
}
