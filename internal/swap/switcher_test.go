package swap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func newTestSwitcher(t *testing.T) *Switcher {
	t.Helper()
	home := t.TempDir()
	backupDir := filepath.Join(home, ".claude-swap-backup")

	s := &Switcher{
		home:           home,
		backupDir:      backupDir,
		sequenceFile:   filepath.Join(backupDir, "sequence.json"),
		configsDir:     filepath.Join(backupDir, "configs"),
		credentialsDir: filepath.Join(backupDir, "credentials"),
		lockFile:       filepath.Join(backupDir, ".lock"),
		platform:       "linux",
		logger:         log.New(io.Discard, "", 0),
	}
	s.setupDirs()
	return s
}

func writeClaudeConfig(t *testing.T, home, email, orgUUID, orgName string) {
	t.Helper()
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)

	config := map[string]interface{}{
		"oauthAccount": map[string]interface{}{
			"emailAddress":     email,
			"organizationUuid": orgUUID,
			"organizationName": orgName,
			"accountUuid":      "uuid-" + email,
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, ".claude.json"), data, 0600)
}

func writeTestCredentials(t *testing.T, home, token string) {
	t.Helper()
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)

	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"%s"}}`, token)
	os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(creds), 0600)
}

// --- Version ---

func TestVersion(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() returned empty string")
	}
	if v != "0.1.0" {
		t.Errorf("Version() = %q, want %q", v, "0.1.0")
	}
}

// --- displayTag ---

func TestDisplayTag(t *testing.T) {
	tests := []struct {
		orgName string
		orgUUID string
		want    string
	}{
		{"Acme Corp", "uuid-123", "Acme Corp"},
		{"", "uuid-123", "personal"},
		{"", "", "personal"},
		{"My Org", "", "My Org"},
	}
	for _, tt := range tests {
		got := displayTag(tt.orgName, tt.orgUUID)
		if got != tt.want {
			t.Errorf("displayTag(%q, %q) = %q, want %q", tt.orgName, tt.orgUUID, got, tt.want)
		}
	}
}

// --- validEmail ---

func TestValidEmail(t *testing.T) {
	valid := []string{
		"user@example.com",
		"first.last@domain.org",
		"user+tag@sub.domain.co",
		"a@b.cc",
	}
	for _, e := range valid {
		if !validEmail(e) {
			t.Errorf("validEmail(%q) = false, want true", e)
		}
	}

	invalid := []string{
		"",
		"not-an-email",
		"@missing.com",
		"user@",
		"user@.com",
		"user@domain.c",
	}
	for _, e := range invalid {
		if validEmail(e) {
			t.Errorf("validEmail(%q) = true, want false", e)
		}
	}
}

// --- extractAccessToken ---

func TestExtractAccessToken(t *testing.T) {
	t.Run("valid credentials", func(t *testing.T) {
		creds := `{"claudeAiOauth":{"accessToken":"tok_abc123","refreshToken":"ref_xyz"}}`
		got := extractAccessToken(creds)
		if got != "tok_abc123" {
			t.Errorf("got %q, want %q", got, "tok_abc123")
		}
	})

	t.Run("missing claudeAiOauth key", func(t *testing.T) {
		creds := `{"otherKey":{"accessToken":"tok_abc123"}}`
		got := extractAccessToken(creds)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("missing accessToken", func(t *testing.T) {
		creds := `{"claudeAiOauth":{"refreshToken":"ref_xyz"}}`
		got := extractAccessToken(creds)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		got := extractAccessToken("not json")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		got := extractAccessToken("")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// --- readJSON / writeJSON ---

func TestReadWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	active := 2
	data := &SequenceData{
		ActiveAccountNumber: &active,
		LastUpdated:         "2026-01-01T00:00:00Z",
		Sequence:            []int{1, 2},
		Accounts: map[string]AccountEntry{
			"1": {Email: "a@test.com", UUID: "u1"},
			"2": {Email: "b@test.com", UUID: "u2", OrganizationName: "Org"},
		},
	}

	if err := writeJSON(path, data); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	got, err := readJSON(path)
	if err != nil {
		t.Fatalf("readJSON: %v", err)
	}

	if *got.ActiveAccountNumber != 2 {
		t.Errorf("ActiveAccountNumber = %d, want 2", *got.ActiveAccountNumber)
	}
	if len(got.Sequence) != 2 {
		t.Errorf("Sequence length = %d, want 2", len(got.Sequence))
	}
	if got.Accounts["1"].Email != "a@test.com" {
		t.Errorf("Account 1 email = %q, want %q", got.Accounts["1"].Email, "a@test.com")
	}
	if got.Accounts["2"].OrganizationName != "Org" {
		t.Errorf("Account 2 org = %q, want %q", got.Accounts["2"].OrganizationName, "Org")
	}
}

func TestReadJSON_NonExistentFile(t *testing.T) {
	got, err := readJSON("/nonexistent/path/file.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil data for missing file, got: %+v", got)
	}
}

func TestReadJSON_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("not json {{{"), 0600)

	_, err := readJSON(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadJSON_NilAccountsInitialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.json")
	os.WriteFile(path, []byte(`{"lastUpdated":"now","sequence":[]}`), 0600)

	got, err := readJSON(path)
	if err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if got.Accounts == nil {
		t.Fatal("Accounts should be initialized to non-nil map")
	}
}

// --- initSequenceFile ---

func TestInitSequenceFile(t *testing.T) {
	sw := newTestSwitcher(t)
	sw.initSequenceFile()

	data, err := readJSON(sw.sequenceFile)
	if err != nil {
		t.Fatalf("readJSON: %v", err)
	}
	if data == nil {
		t.Fatal("sequence data is nil after init")
	}
	if data.ActiveAccountNumber != nil {
		t.Error("ActiveAccountNumber should be nil on fresh init")
	}
	if len(data.Sequence) != 0 {
		t.Errorf("Sequence should be empty, got %v", data.Sequence)
	}
	if len(data.Accounts) != 0 {
		t.Errorf("Accounts should be empty, got %v", data.Accounts)
	}
}

func TestInitSequenceFile_DoesNotOverwrite(t *testing.T) {
	sw := newTestSwitcher(t)

	active := 5
	existing := &SequenceData{
		ActiveAccountNumber: &active,
		LastUpdated:         "existing",
		Sequence:            []int{5},
		Accounts:            map[string]AccountEntry{"5": {Email: "keep@me.com"}},
	}
	writeJSON(sw.sequenceFile, existing)

	sw.initSequenceFile()

	data, _ := readJSON(sw.sequenceFile)
	if *data.ActiveAccountNumber != 5 {
		t.Error("initSequenceFile overwrote existing data")
	}
}

// --- nextAccountNumber ---

func TestNextAccountNumber_Empty(t *testing.T) {
	sw := newTestSwitcher(t)
	sw.initSequenceFile()

	if got := sw.nextAccountNumber(); got != 1 {
		t.Errorf("nextAccountNumber() = %d, want 1", got)
	}
}

func TestNextAccountNumber_WithAccounts(t *testing.T) {
	sw := newTestSwitcher(t)
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1, 3},
		Accounts: map[string]AccountEntry{
			"1": {Email: "a@test.com"},
			"3": {Email: "b@test.com"},
		},
	}
	writeJSON(sw.sequenceFile, data)

	if got := sw.nextAccountNumber(); got != 4 {
		t.Errorf("nextAccountNumber() = %d, want 4", got)
	}
}

// --- accountExists ---

func TestAccountExists(t *testing.T) {
	sw := newTestSwitcher(t)
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1, 2},
		Accounts: map[string]AccountEntry{
			"1": {Email: "a@test.com", OrganizationUUID: "org-1"},
			"2": {Email: "a@test.com", OrganizationUUID: "org-2"},
		},
	}
	writeJSON(sw.sequenceFile, data)

	if !sw.accountExists("a@test.com", "org-1") {
		t.Error("should find account with matching email and org")
	}
	if !sw.accountExists("a@test.com", "org-2") {
		t.Error("should find account with same email but different org")
	}
	if sw.accountExists("a@test.com", "org-3") {
		t.Error("should not find account with non-existent org")
	}
	if sw.accountExists("b@test.com", "org-1") {
		t.Error("should not find account with non-existent email")
	}
}

// --- resolveIdentifier ---

func TestResolveIdentifier_ByNumber(t *testing.T) {
	sw := newTestSwitcher(t)
	sw.initSequenceFile()

	got, err := sw.resolveIdentifier("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "42" {
		t.Errorf("got %q, want %q", got, "42")
	}
}

func TestResolveIdentifier_ByEmail(t *testing.T) {
	sw := newTestSwitcher(t)
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1},
		Accounts: map[string]AccountEntry{
			"1": {Email: "found@test.com"},
		},
	}
	writeJSON(sw.sequenceFile, data)

	got, err := sw.resolveIdentifier("found@test.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1" {
		t.Errorf("got %q, want %q", got, "1")
	}
}

func TestResolveIdentifier_EmailNotFound(t *testing.T) {
	sw := newTestSwitcher(t)
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1},
		Accounts:    map[string]AccountEntry{"1": {Email: "other@test.com"}},
	}
	writeJSON(sw.sequenceFile, data)

	got, err := sw.resolveIdentifier("missing@test.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveIdentifier_AmbiguousEmail(t *testing.T) {
	sw := newTestSwitcher(t)
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1, 2},
		Accounts: map[string]AccountEntry{
			"1": {Email: "dup@test.com", OrganizationName: "Org A"},
			"2": {Email: "dup@test.com", OrganizationName: "Org B"},
		},
	}
	writeJSON(sw.sequenceFile, data)

	_, err := sw.resolveIdentifier("dup@test.com")
	if err == nil {
		t.Fatal("expected error for ambiguous email")
	}
	if got := err.Error(); !contains(got, "ambiguous") {
		t.Errorf("error should mention ambiguous, got: %s", got)
	}
}

// --- Account config read/write ---

func TestAccountConfig_ReadWrite(t *testing.T) {
	sw := newTestSwitcher(t)
	config := `{"oauthAccount":{"emailAddress":"test@test.com"}}`

	sw.writeAccountConfig("1", "test@test.com", config)

	got := sw.readAccountConfig("1", "test@test.com")
	if got != config {
		t.Errorf("readAccountConfig mismatch:\ngot:  %s\nwant: %s", got, config)
	}
}

func TestAccountConfig_ReadMissing(t *testing.T) {
	sw := newTestSwitcher(t)
	got := sw.readAccountConfig("99", "nope@test.com")
	if got != "" {
		t.Errorf("expected empty for missing config, got %q", got)
	}
}

// --- File-based credentials (linux platform) ---

func TestAccountCreds_ReadWrite_Linux(t *testing.T) {
	sw := newTestSwitcher(t)
	creds := `{"claudeAiOauth":{"accessToken":"secret-token"}}`

	sw.writeAccountCreds("1", "user@test.com", creds)

	got := sw.readAccountCreds("1", "user@test.com")
	if got != creds {
		t.Errorf("readAccountCreds mismatch:\ngot:  %s\nwant: %s", got, creds)
	}
}

func TestAccountCreds_StoredAsBase64(t *testing.T) {
	sw := newTestSwitcher(t)
	creds := `{"claudeAiOauth":{"accessToken":"tok123"}}`

	sw.writeAccountCreds("1", "user@test.com", creds)

	raw, err := os.ReadFile(filepath.Join(sw.credentialsDir, ".creds-1-user@test.com.enc"))
	if err != nil {
		t.Fatalf("failed to read creds file: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("creds file is not valid base64: %v", err)
	}
	if string(decoded) != creds {
		t.Errorf("decoded creds = %q, want %q", string(decoded), creds)
	}
}

func TestAccountCreds_Delete(t *testing.T) {
	sw := newTestSwitcher(t)
	sw.writeAccountCreds("1", "user@test.com", "some-creds")

	sw.deleteAccountCreds("1", "user@test.com")

	got := sw.readAccountCreds("1", "user@test.com")
	if got != "" {
		t.Errorf("expected empty after delete, got %q", got)
	}
}

func TestAccountCreds_ReadMissing(t *testing.T) {
	sw := newTestSwitcher(t)
	got := sw.readAccountCreds("99", "nope@test.com")
	if got != "" {
		t.Errorf("expected empty for missing creds, got %q", got)
	}
}

// --- Credentials (file-based, linux) ---

func TestCredentials_ReadWrite_Linux(t *testing.T) {
	sw := newTestSwitcher(t)
	creds := `{"claudeAiOauth":{"accessToken":"live-tok"}}`

	writeTestCredentials(t, sw.home, "live-tok")

	got, err := sw.readCredentials()
	if err != nil {
		t.Fatalf("readCredentials: %v", err)
	}
	if got != creds {
		t.Errorf("got %q, want %q", got, creds)
	}
}

func TestCredentials_ReadMissing(t *testing.T) {
	sw := newTestSwitcher(t)
	got, err := sw.readCredentials()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for missing credentials, got %q", got)
	}
}

func TestCredentials_Write_Linux(t *testing.T) {
	sw := newTestSwitcher(t)
	creds := `{"claudeAiOauth":{"accessToken":"new-tok"}}`

	err := sw.writeCredentials(creds)
	if err != nil {
		t.Fatalf("writeCredentials: %v", err)
	}

	got, _ := sw.readCredentials()
	if got != creds {
		t.Errorf("got %q, want %q", got, creds)
	}
}

// --- currentAccount ---

func TestCurrentAccount(t *testing.T) {
	sw := newTestSwitcher(t)
	writeClaudeConfig(t, sw.home, "user@test.com", "org-uuid", "My Org")

	email, orgUUID, ok := sw.currentAccount()
	if !ok {
		t.Fatal("currentAccount returned ok=false")
	}
	if email != "user@test.com" {
		t.Errorf("email = %q, want %q", email, "user@test.com")
	}
	if orgUUID != "org-uuid" {
		t.Errorf("orgUUID = %q, want %q", orgUUID, "org-uuid")
	}
}

func TestCurrentAccount_NoConfig(t *testing.T) {
	sw := newTestSwitcher(t)
	_, _, ok := sw.currentAccount()
	if ok {
		t.Error("expected ok=false when no config exists")
	}
}

func TestCurrentAccount_NoOauthAccount(t *testing.T) {
	sw := newTestSwitcher(t)
	claudeDir := filepath.Join(sw.home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(`{"someOtherKey":"val"}`), 0600)

	_, _, ok := sw.currentAccount()
	if ok {
		t.Error("expected ok=false when oauthAccount is missing")
	}
}

// --- claudeConfigPath ---

func TestClaudeConfigPath_PrefersFileWithOauthAccount(t *testing.T) {
	sw := newTestSwitcher(t)

	// Write config without oauthAccount at primary path
	claudeDir := filepath.Join(sw.home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(`{"theme":"dark"}`), 0600)

	// Write config with oauthAccount at fallback path
	config := `{"oauthAccount":{"emailAddress":"user@test.com"}}`
	os.WriteFile(filepath.Join(sw.home, ".claude.json"), []byte(config), 0600)

	got := sw.claudeConfigPath()
	want := filepath.Join(sw.home, ".claude.json")
	if got != want {
		t.Errorf("claudeConfigPath() = %q, want %q", got, want)
	}
}

func TestClaudeConfigPath_FallbackWhenNoneExist(t *testing.T) {
	sw := newTestSwitcher(t)

	got := sw.claudeConfigPath()
	want := filepath.Join(sw.home, ".claude.json")
	if got != want {
		t.Errorf("claudeConfigPath() = %q, want fallback %q", got, want)
	}
}

// --- File locking ---

func TestAcquireAndReleaseLock(t *testing.T) {
	sw := newTestSwitcher(t)

	lock, err := sw.acquireLock()
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}

	lock.release()

	lock2, err := sw.acquireLock()
	if err != nil {
		t.Fatalf("second acquireLock after release: %v", err)
	}
	lock2.release()
}

// --- AddAccount (integration) ---

func TestAddAccount_NewAccount(t *testing.T) {
	sw := newTestSwitcher(t)
	writeClaudeConfig(t, sw.home, "new@test.com", "org-1", "Test Org")
	writeTestCredentials(t, sw.home, "tok-new")

	err := sw.AddAccount()
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	data := sw.getSequence()
	if data == nil {
		t.Fatal("sequence data is nil")
	}
	if len(data.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(data.Accounts))
	}

	acc := data.Accounts["1"]
	if acc.Email != "new@test.com" {
		t.Errorf("email = %q, want %q", acc.Email, "new@test.com")
	}
	if acc.OrganizationUUID != "org-1" {
		t.Errorf("orgUUID = %q, want %q", acc.OrganizationUUID, "org-1")
	}
	if acc.OrganizationName != "Test Org" {
		t.Errorf("orgName = %q, want %q", acc.OrganizationName, "Test Org")
	}
	if *data.ActiveAccountNumber != 1 {
		t.Errorf("ActiveAccountNumber = %d, want 1", *data.ActiveAccountNumber)
	}
	if len(data.Sequence) != 1 || data.Sequence[0] != 1 {
		t.Errorf("Sequence = %v, want [1]", data.Sequence)
	}

	// Verify credentials and config were backed up
	creds := sw.readAccountCreds("1", "new@test.com")
	if creds == "" {
		t.Error("account credentials were not backed up")
	}
	if extractAccessToken(creds) != "tok-new" {
		t.Error("backed up credentials don't match")
	}

	cfg := sw.readAccountConfig("1", "new@test.com")
	if cfg == "" {
		t.Error("account config was not backed up")
	}
}

func TestAddAccount_UpdateExisting(t *testing.T) {
	sw := newTestSwitcher(t)
	writeClaudeConfig(t, sw.home, "user@test.com", "org-1", "Org")
	writeTestCredentials(t, sw.home, "tok-old")

	sw.AddAccount()

	// Update credentials and re-add
	writeTestCredentials(t, sw.home, "tok-new")
	err := sw.AddAccount()
	if err != nil {
		t.Fatalf("AddAccount update: %v", err)
	}

	data := sw.getSequence()
	if len(data.Accounts) != 1 {
		t.Fatalf("expected 1 account after update, got %d", len(data.Accounts))
	}

	creds := sw.readAccountCreds("1", "user@test.com")
	if extractAccessToken(creds) != "tok-new" {
		t.Error("credentials were not updated")
	}
}

func TestAddAccount_MultipleAccounts(t *testing.T) {
	sw := newTestSwitcher(t)

	// Add first account
	writeClaudeConfig(t, sw.home, "first@test.com", "org-1", "Org1")
	writeTestCredentials(t, sw.home, "tok-1")
	sw.AddAccount()

	// Add second account
	writeClaudeConfig(t, sw.home, "second@test.com", "org-2", "Org2")
	writeTestCredentials(t, sw.home, "tok-2")
	sw.AddAccount()

	data := sw.getSequence()
	if len(data.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(data.Accounts))
	}
	if len(data.Sequence) != 2 {
		t.Fatalf("expected sequence [1,2], got %v", data.Sequence)
	}
	if *data.ActiveAccountNumber != 2 {
		t.Errorf("active should be 2, got %d", *data.ActiveAccountNumber)
	}
}

func TestAddAccount_NoActiveAccount(t *testing.T) {
	sw := newTestSwitcher(t)
	// No Claude config written
	err := sw.AddAccount()
	if err == nil {
		t.Fatal("expected error when no active account")
	}
}

// --- performSwitch (integration) ---

func TestPerformSwitch(t *testing.T) {
	sw := newTestSwitcher(t)

	// Add two accounts
	writeClaudeConfig(t, sw.home, "acc1@test.com", "org-1", "Org1")
	writeTestCredentials(t, sw.home, "tok-1")
	sw.AddAccount()

	writeClaudeConfig(t, sw.home, "acc2@test.com", "org-2", "Org2")
	writeTestCredentials(t, sw.home, "tok-2")
	sw.AddAccount()

	// Currently active: account 2. Switch to account 1.
	err := sw.performSwitch("1")
	if err != nil {
		t.Fatalf("performSwitch: %v", err)
	}

	// Verify active account changed
	data := sw.getSequence()
	if *data.ActiveAccountNumber != 1 {
		t.Errorf("active = %d, want 1", *data.ActiveAccountNumber)
	}

	// Verify live credentials are now account 1's
	email, _, ok := sw.currentAccount()
	if !ok {
		t.Fatal("no current account after switch")
	}
	if email != "acc1@test.com" {
		t.Errorf("current email = %q, want %q", email, "acc1@test.com")
	}

	liveCreds, _ := sw.readCredentials()
	if extractAccessToken(liveCreds) != "tok-1" {
		t.Error("live credentials don't match account 1")
	}

	// Verify account 2's credentials were backed up during switch
	backed := sw.readAccountCreds("2", "acc2@test.com")
	if extractAccessToken(backed) != "tok-2" {
		t.Error("account 2 credentials were not backed up")
	}
}

func TestPerformSwitch_MissingBackup(t *testing.T) {
	sw := newTestSwitcher(t)

	writeClaudeConfig(t, sw.home, "acc1@test.com", "org-1", "Org1")
	writeTestCredentials(t, sw.home, "tok-1")
	sw.AddAccount()

	// Add account 2 to sequence but don't write backup files
	data := sw.getSequence()
	data.Accounts["2"] = AccountEntry{Email: "acc2@test.com"}
	data.Sequence = append(data.Sequence, 2)
	writeJSON(sw.sequenceFile, data)

	err := sw.performSwitch("2")
	if err == nil {
		t.Fatal("expected error when backup data is missing")
	}
}

// --- migrateOrgFields ---

func TestMigrateOrgFields(t *testing.T) {
	sw := newTestSwitcher(t)

	// Setup: account with empty org fields
	data := &SequenceData{
		LastUpdated: "now",
		Sequence:    []int{1},
		Accounts: map[string]AccountEntry{
			"1": {Email: "user@test.com", OrganizationUUID: "", OrganizationName: ""},
		},
	}
	writeJSON(sw.sequenceFile, data)

	// Write config backup with org info
	config := `{"oauthAccount":{"emailAddress":"user@test.com","organizationUuid":"org-migrated","organizationName":"Migrated Org"}}`
	sw.writeAccountConfig("1", "user@test.com", config)

	// Write live config (different account)
	writeClaudeConfig(t, sw.home, "other@test.com", "", "")

	sw.migrateOrgFields()

	updated := sw.getSequence()
	acc := updated.Accounts["1"]
	if acc.OrganizationUUID != "org-migrated" {
		t.Errorf("orgUUID = %q, want %q", acc.OrganizationUUID, "org-migrated")
	}
	if acc.OrganizationName != "Migrated Org" {
		t.Errorf("orgName = %q, want %q", acc.OrganizationName, "Migrated Org")
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
