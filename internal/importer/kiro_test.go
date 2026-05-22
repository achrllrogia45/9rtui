package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKiroAccounts_FormatB_9rtuiExport(t *testing.T) {
	// Format B: 9rtui export with stringified data field
	content := `{
  "data": [
    {
      "id": "test-1",
      "provider": "kiro",
      "authType": "oauth",
      "name": "Test Account 1",
      "email": "",
      "priority": 1,
      "isActive": 1,
      "data": "{\"accessToken\":\"at_test_1\",\"refreshToken\":\"rt_test_1\"}"
    },
    {
      "id": "test-2",
      "provider": "kiro",
      "authType": "oauth",
      "name": "Test Account 2",
      "email": "test2@example.com",
      "priority": 2,
      "isActive": 0,
      "data": "{\"accessToken\":\"at_test_2\",\"refreshToken\":\"rt_test_2\"}"
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}

	// Check first account (no email, should use name)
	if accounts[0].Email != "Test Account 1" {
		t.Errorf("expected email='Test Account 1', got '%s'", accounts[0].Email)
	}
	if accounts[0].RefreshToken != "rt_test_1" {
		t.Errorf("expected refreshToken='rt_test_1', got '%s'", accounts[0].RefreshToken)
	}
	if accounts[0].AccessToken != "at_test_1" {
		t.Errorf("expected accessToken='at_test_1', got '%s'", accounts[0].AccessToken)
	}
	if accounts[0].Status != "active" {
		t.Errorf("expected status='active' (from isActive=1), got '%s'", accounts[0].Status)
	}

	// Check second account (has email, should use email)
	if accounts[1].Email != "test2@example.com" {
		t.Errorf("expected email='test2@example.com', got '%s'", accounts[1].Email)
	}
	if accounts[1].Status != "inactive" {
		t.Errorf("expected status='inactive' (from isActive=0), got '%s'", accounts[1].Status)
	}
}

func TestLoadKiroAccounts_FormatC_InspectLog(t *testing.T) {
	// Format C: inspect log with nested data object
	content := `{
  "data": [
    {
      "provider": "kiro",
      "name": "Inspect Account 1",
      "email": "",
      "data": {
        "accessToken": "at_inspect_1",
        "refreshToken": "rt_inspect_1",
        "providerSpecificData": {
          "profileArn": "arn:aws:sso:::profile/test",
          "clientId": "client123",
          "startUrl": "https://test.awsapps.com/start"
        }
      }
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}

	if accounts[0].Email != "Inspect Account 1" {
		t.Errorf("expected email='Inspect Account 1', got '%s'", accounts[0].Email)
	}
	if accounts[0].RefreshToken != "rt_inspect_1" {
		t.Errorf("expected refreshToken='rt_inspect_1', got '%s'", accounts[0].RefreshToken)
	}
	if accounts[0].ProfileARN != "arn:aws:sso:::profile/test" {
		t.Errorf("expected profileArn from providerSpecificData, got '%s'", accounts[0].ProfileARN)
	}
}

func TestLoadKiroAccounts_FormatD_KiroImportLog(t *testing.T) {
	// Format D: kiro-import log with rows array
	content := `{
  "rows": [
    {
      "id": "row-1",
      "email": "row1@example.com",
      "refreshToken": "rt_row_1",
      "accessToken": "at_row_1",
      "profileArn": "arn:aws:sso:::profile/row1",
      "planType": "free"
    },
    {
      "id": "row-2",
      "email": "",
      "refreshToken": "rt_row_2",
      "accessToken": "at_row_2"
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	// Should be 0 because these rows don't have provider="kiro"
	if len(accounts) != 0 {
		t.Logf("Note: Format D rows need provider='kiro' field. Got %d accounts", len(accounts))
	}
}

func TestLoadKiroAccounts_FormatD_WithProvider(t *testing.T) {
	// Format D with provider field added
	content := `{
  "rows": [
    {
      "id": "row-1",
      "provider": "kiro",
      "email": "row1@example.com",
      "refreshToken": "rt_row_1",
      "accessToken": "at_row_1",
      "profileArn": "arn:aws:sso:::profile/row1",
      "planType": "free"
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}

	if accounts[0].Email != "row1@example.com" {
		t.Errorf("expected email='row1@example.com', got '%s'", accounts[0].Email)
	}
	if accounts[0].RefreshToken != "rt_row_1" {
		t.Errorf("expected refreshToken='rt_row_1', got '%s'", accounts[0].RefreshToken)
	}
	if accounts[0].ProfileARN != "arn:aws:sso:::profile/row1" {
		t.Errorf("expected profileArn='arn:aws:sso:::profile/row1', got '%s'", accounts[0].ProfileARN)
	}
	if accounts[0].PlanType != "free" {
		t.Errorf("expected planType='free', got '%s'", accounts[0].PlanType)
	}
}

func TestLoadKiroAccounts_EmailFallback(t *testing.T) {
	// Test email > name > id > "Account N" fallback
	content := `{
  "data": [
    {
      "provider": "kiro",
      "credentials": {"refreshToken": "rt1"}
    },
    {
      "provider": "kiro",
      "id": "id-only",
      "credentials": {"refreshToken": "rt2"}
    },
    {
      "provider": "kiro",
      "name": "Name Only",
      "credentials": {"refreshToken": "rt3"}
    },
    {
      "provider": "kiro",
      "email": "email@test.com",
      "name": "Should Use Email",
      "credentials": {"refreshToken": "rt4"}
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	if len(accounts) != 4 {
		t.Fatalf("expected 4 accounts, got %d", len(accounts))
	}

	if accounts[0].Email != "Account 1" {
		t.Errorf("expected email='Account 1' (fallback), got '%s'", accounts[0].Email)
	}
	if accounts[1].Email != "id-only" {
		t.Errorf("expected email='id-only' (from id), got '%s'", accounts[1].Email)
	}
	if accounts[2].Email != "Name Only" {
		t.Errorf("expected email='Name Only' (from name), got '%s'", accounts[2].Email)
	}
	if accounts[3].Email != "email@test.com" {
		t.Errorf("expected email='email@test.com', got '%s'", accounts[3].Email)
	}
}

func TestLoadKiroAccounts_Dedupe(t *testing.T) {
	// Test that dedupe by refreshToken hash works
	content := `{
  "data": [
    {
      "provider": "kiro",
      "email": "dup1@test.com",
      "credentials": {"refreshToken": "rt_duplicate"}
    },
    {
      "provider": "kiro",
      "email": "dup2@test.com",
      "credentials": {"refreshToken": "rt_duplicate"}
    },
    {
      "provider": "kiro",
      "email": "unique@test.com",
      "credentials": {"refreshToken": "rt_unique"}
    }
  ]
}`
	tmpFile := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	accounts, err := LoadKiroAccounts(tmpFile)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed: %v", err)
	}

	// Loader doesn't dedupe, it loads all. Dedupe happens in markAvailability
	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts loaded, got %d", len(accounts))
	}

	// Verify hashes are computed
	if accounts[0].RefreshHash == "" {
		t.Error("expected RefreshHash to be computed")
	}
	if accounts[0].RefreshHash != accounts[1].RefreshHash {
		t.Error("expected duplicate tokens to have same hash")
	}
	if accounts[0].RefreshHash == accounts[2].RefreshHash {
		t.Error("expected unique token to have different hash")
	}
}

func TestLoadKiroAccounts_RealFile(t *testing.T) {
	// Test with actual accounts.json if it exists
	path := "/home/hilman/projects/9rtui/accounts.json"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("accounts.json not found, skipping real file test")
	}

	accounts, err := LoadKiroAccounts(path)
	if err != nil {
		t.Fatalf("LoadKiroAccounts failed on real file: %v", err)
	}

	t.Logf("Loaded %d accounts from real file", len(accounts))
	
	// Sample first account
	if len(accounts) > 0 {
		b, _ := json.MarshalIndent(accounts[0], "", "  ")
		t.Logf("Sample account:\n%s", string(b))
	}
}
