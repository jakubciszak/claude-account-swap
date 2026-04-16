package swap

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "0.1.0"

func Version() string { return version }

// --- Data types ---

type AccountEntry struct {
	Email            string `json:"email"`
	UUID             string `json:"uuid"`
	OrganizationUUID string `json:"organizationUuid"`
	OrganizationName string `json:"organizationName"`
	Added            string `json:"added"`
}

type SequenceData struct {
	ActiveAccountNumber *int                    `json:"activeAccountNumber"`
	LastUpdated         string                  `json:"lastUpdated"`
	Sequence            []int                   `json:"sequence"`
	Accounts            map[string]AccountEntry `json:"accounts"`
}

// --- Switcher ---

type Switcher struct {
	home           string
	backupDir      string
	sequenceFile   string
	configsDir     string
	credentialsDir string
	lockFile       string
	platform       string // "darwin", "linux", "wsl", "windows"
	logger         *log.Logger
}

func NewSwitcher(debug bool) *Switcher {
	home, _ := os.UserHomeDir()
	backupDir := filepath.Join(home, ".claude-swap-backup")

	s := &Switcher{
		home:           home,
		backupDir:      backupDir,
		sequenceFile:   filepath.Join(backupDir, "sequence.json"),
		configsDir:     filepath.Join(backupDir, "configs"),
		credentialsDir: filepath.Join(backupDir, "credentials"),
		lockFile:       filepath.Join(backupDir, ".lock"),
		platform:       detectPlatform(),
	}
	s.logger = setupLogger(backupDir, debug)
	return s
}

func detectPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	case "linux":
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			return "wsl"
		}
		return "linux"
	}
	return "unknown"
}

func setupLogger(dir string, debug bool) *log.Logger {
	os.MkdirAll(dir, 0700)
	f, err := os.OpenFile(filepath.Join(dir, "claude-swap.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		if debug {
			return log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
		}
		return log.New(io.Discard, "", 0)
	}
	if debug {
		w := io.MultiWriter(f, os.Stderr)
		return log.New(w, "", log.LstdFlags)
	}
	return log.New(f, "", log.LstdFlags)
}

func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// --- File locking ---

type fileLock struct {
	f *os.File
}

func (s *Switcher) acquireLock() (*fileLock, error) {
	os.MkdirAll(filepath.Dir(s.lockFile), 0700)
	f, err := os.OpenFile(s.lockFile, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock file: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &fileLock{f: f}, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("failed to acquire lock - another instance may be running")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *fileLock) release() {
	if l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}

// --- Directory setup ---

func (s *Switcher) setupDirs() {
	for _, d := range []string{s.backupDir, s.configsDir, s.credentialsDir} {
		os.MkdirAll(d, 0700)
	}
}

// --- JSON helpers ---

func readJSON(path string) (*SequenceData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var seq SequenceData
	if err := json.Unmarshal(data, &seq); err != nil {
		return nil, err
	}
	if seq.Accounts == nil {
		seq.Accounts = make(map[string]AccountEntry)
	}
	return &seq, nil
}

func writeJSON(path string, data *SequenceData) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	// validate
	var check SequenceData
	raw, _ := os.ReadFile(tmp)
	if err := json.Unmarshal(raw, &check); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("generated invalid JSON")
	}
	return os.Rename(tmp, path)
}

// --- Claude config ---

func (s *Switcher) claudeConfigPath() string {
	primary := filepath.Join(s.home, ".claude", ".claude.json")
	fallback := filepath.Join(s.home, ".claude.json")

	type candidate struct {
		path string
		mt   time.Time
	}
	var candidates []candidate

	for _, p := range []string{primary, fallback} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m map[string]interface{}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if _, ok := m["oauthAccount"]; !ok {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{p, fi.ModTime()})
	}

	if len(candidates) == 0 {
		return fallback
	}
	if len(candidates) == 1 {
		return candidates[0].path
	}
	if candidates[0].mt.After(candidates[1].mt) {
		return candidates[0].path
	}
	return candidates[1].path
}

func (s *Switcher) currentAccount() (email, orgUUID string, ok bool) {
	configPath := s.claudeConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return "", "", false
	}
	oauthRaw, exists := m["oauthAccount"]
	if !exists {
		return "", "", false
	}
	var oauth struct {
		Email            string `json:"emailAddress"`
		OrganizationUUID string `json:"organizationUuid"`
	}
	if json.Unmarshal(oauthRaw, &oauth) != nil || oauth.Email == "" {
		return "", "", false
	}
	return oauth.Email, oauth.OrganizationUUID, true
}

// --- Credentials (macOS keychain / file-based) ---

func (s *Switcher) readCredentials() (string, error) {
	if s.platform == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Claude Code-credentials", "-w").CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "could not be found") {
				return "", nil
			}
			return "", fmt.Errorf("keychain read failed: %s", strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	// Linux/WSL/Windows: file-based
	credFile := filepath.Join(s.home, ".claude", ".credentials.json")
	data, err := os.ReadFile(credFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (s *Switcher) writeCredentials(creds string) error {
	if s.platform == "darwin" {
		user := os.Getenv("USER")
		if user == "" {
			user = "user"
		}
		out, err := exec.Command("security", "add-generic-password", "-U",
			"-s", "Claude Code-credentials", "-a", user, "-w", creds).CombinedOutput()
		if err != nil {
			return fmt.Errorf("keychain write failed: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	credDir := filepath.Join(s.home, ".claude")
	os.MkdirAll(credDir, 0700)
	return os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte(creds), 0600)
}

func (s *Switcher) readAccountCreds(num, email string) string {
	if s.platform == "linux" || s.platform == "wsl" {
		f := filepath.Join(s.credentialsDir, fmt.Sprintf(".creds-%s-%s.enc", num, email))
		data, err := os.ReadFile(f)
		if err != nil {
			return ""
		}
		decoded, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil {
			return ""
		}
		return string(decoded)
	}
	// macOS: use keychain
	username := fmt.Sprintf("account-%s-%s", num, email)
	out, err := exec.Command("security", "find-generic-password",
		"-s", "claude-code", "-a", username, "-w").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Switcher) writeAccountCreds(num, email, creds string) {
	if s.platform == "linux" || s.platform == "wsl" {
		f := filepath.Join(s.credentialsDir, fmt.Sprintf(".creds-%s-%s.enc", num, email))
		encoded := base64.StdEncoding.EncodeToString([]byte(creds))
		os.WriteFile(f, []byte(encoded), 0600)
		return
	}
	username := fmt.Sprintf("account-%s-%s", num, email)
	exec.Command("security", "add-generic-password", "-U",
		"-s", "claude-code", "-a", username, "-w", creds).Run()
}

func (s *Switcher) deleteAccountCreds(num, email string) {
	if s.platform == "linux" || s.platform == "wsl" {
		f := filepath.Join(s.credentialsDir, fmt.Sprintf(".creds-%s-%s.enc", num, email))
		os.Remove(f)
		return
	}
	username := fmt.Sprintf("account-%s-%s", num, email)
	exec.Command("security", "delete-generic-password",
		"-s", "claude-code", "-a", username).Run()
}

// --- Account config backup ---

func (s *Switcher) readAccountConfig(num, email string) string {
	f := filepath.Join(s.configsDir, fmt.Sprintf(".claude-config-%s-%s.json", num, email))
	data, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *Switcher) writeAccountConfig(num, email, config string) {
	f := filepath.Join(s.configsDir, fmt.Sprintf(".claude-config-%s-%s.json", num, email))
	os.WriteFile(f, []byte(config), 0600)
}

// --- Sequence helpers ---

func (s *Switcher) initSequenceFile() {
	if _, err := os.Stat(s.sequenceFile); err == nil {
		return
	}
	data := &SequenceData{
		ActiveAccountNumber: nil,
		LastUpdated:         timestamp(),
		Sequence:            []int{},
		Accounts:            make(map[string]AccountEntry),
	}
	writeJSON(s.sequenceFile, data)
}

func (s *Switcher) getSequence() *SequenceData {
	data, err := readJSON(s.sequenceFile)
	if err != nil {
		s.logger.Printf("Error reading sequence: %v", err)
		return nil
	}
	return data
}

func (s *Switcher) nextAccountNumber() int {
	data := s.getSequence()
	if data == nil || len(data.Accounts) == 0 {
		return 1
	}
	max := 0
	for k := range data.Accounts {
		n, _ := strconv.Atoi(k)
		if n > max {
			max = n
		}
	}
	return max + 1
}

func (s *Switcher) accountExists(email, orgUUID string) bool {
	data := s.getSequence()
	if data == nil {
		return false
	}
	for _, acc := range data.Accounts {
		if acc.Email == email && acc.OrganizationUUID == orgUUID {
			return true
		}
	}
	return false
}

func displayTag(orgName, orgUUID string) string {
	if orgName != "" {
		return orgName
	}
	return "personal"
}

func validEmail(e string) bool {
	re := regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	return re.MatchString(e)
}

func (s *Switcher) resolveIdentifier(id string) (string, error) {
	if _, err := strconv.Atoi(id); err == nil {
		return id, nil
	}
	data := s.getSequence()
	if data == nil {
		return "", nil
	}
	var matches []string
	for num, acc := range data.Accounts {
		if acc.Email == id {
			matches = append(matches, num)
		}
	}
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	details := make([]string, len(matches))
	for i, num := range matches {
		acc := data.Accounts[num]
		details[i] = fmt.Sprintf("%s [%s]", num, displayTag(acc.OrganizationName, acc.OrganizationUUID))
	}
	return "", fmt.Errorf("email '%s' is ambiguous — matches accounts: %s. Use account number instead", id, strings.Join(details, ", "))
}

// --- Usage API ---

type usageResult struct {
	FiveHour *usageBucket `json:"five_hour,omitempty"`
	SevenDay *usageBucket `json:"seven_day,omitempty"`
}

type usageBucket struct {
	Pct       float64 `json:"utilization"`
	ResetsAt  string  `json:"resets_at,omitempty"`
	Countdown string  `json:"-"`
	Clock     string  `json:"-"`
}

func extractAccessToken(creds string) string {
	var m map[string]map[string]interface{}
	if json.Unmarshal([]byte(creds), &m) != nil {
		return ""
	}
	oauth, ok := m["claudeAiOauth"]
	if !ok {
		return ""
	}
	tok, _ := oauth["accessToken"].(string)
	return tok
}

func formatReset(resetsAt string) (countdown, clock string) {
	t, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil {
		// try ISO format without timezone
		t, err = time.Parse("2006-01-02T15:04:05Z", resetsAt)
		if err != nil {
			return "", ""
		}
	}
	now := time.Now().UTC()
	rem := t.Sub(now)
	if rem < 0 {
		rem = 0
	}
	total := int(rem.Seconds())
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60

	if days > 0 {
		countdown = fmt.Sprintf("%dd %dh", days, hours)
	} else if hours > 0 {
		countdown = fmt.Sprintf("%dh %dm", hours, minutes)
	} else {
		countdown = fmt.Sprintf("%dm", minutes)
	}

	local := t.Local()
	nowLocal := now.Local()
	if local.YearDay() == nowLocal.YearDay() && local.Year() == nowLocal.Year() {
		clock = local.Format("15:04")
	} else {
		clock = local.Format("Jan 2 15:04")
	}
	return
}

func fetchUsage(token string) *usageResult {
	if token == "" {
		return nil
	}
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var raw struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return nil
	}

	result := &usageResult{}
	if raw.FiveHour != nil {
		cd, cl := formatReset(raw.FiveHour.ResetsAt)
		result.FiveHour = &usageBucket{Pct: raw.FiveHour.Utilization, Countdown: cd, Clock: cl}
	}
	if raw.SevenDay != nil {
		cd, cl := formatReset(raw.SevenDay.ResetsAt)
		result.SevenDay = &usageBucket{Pct: raw.SevenDay.Utilization, Countdown: cd, Clock: cl}
	}
	return result
}

// --- Container detection ---

func (s *Switcher) isContainer() bool {
	if os.Getenv("CONTAINER") != "" || os.Getenv("container") != "" {
		return true
	}
	if s.platform == "windows" {
		return false
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		for _, kw := range []string{"docker", "lxc", "containerd", "kubepods"} {
			if strings.Contains(content, kw) {
				return true
			}
		}
	}
	return false
}

// --- Migration ---

func (s *Switcher) migrateOrgFields() {
	data := s.getSequence()
	if data == nil {
		return
	}
	needsMigration := false
	for _, acc := range data.Accounts {
		if acc.OrganizationUUID == "" && acc.OrganizationName == "" {
			// check if field was never set by looking at raw JSON
			// simplified: just run migration anyway, it's idempotent
			needsMigration = true
			break
		}
	}
	if !needsMigration {
		return
	}

	// Read live config for active account
	var liveEmail, liveOrgUUID, liveOrgName string
	configPath := s.claudeConfigPath()
	if raw, err := os.ReadFile(configPath); err == nil {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			if oauthRaw, ok := m["oauthAccount"]; ok {
				var oauth struct {
					Email    string `json:"emailAddress"`
					OrgUUID  string `json:"organizationUuid"`
					OrgName  string `json:"organizationName"`
				}
				json.Unmarshal(oauthRaw, &oauth)
				liveEmail = oauth.Email
				liveOrgUUID = oauth.OrgUUID
				liveOrgName = oauth.OrgName
			}
		}
	}

	updated := false
	for num, acc := range data.Accounts {
		if acc.Email == liveEmail && liveEmail != "" {
			acc.OrganizationUUID = liveOrgUUID
			acc.OrganizationName = liveOrgName
			data.Accounts[num] = acc
			updated = true
			continue
		}
		cfgText := s.readAccountConfig(num, acc.Email)
		if cfgText != "" {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(cfgText), &m) == nil {
				if oauthRaw, ok := m["oauthAccount"]; ok {
					var oauth struct {
						OrgUUID string `json:"organizationUuid"`
						OrgName string `json:"organizationName"`
					}
					json.Unmarshal(oauthRaw, &oauth)
					acc.OrganizationUUID = oauth.OrgUUID
					acc.OrganizationName = oauth.OrgName
					data.Accounts[num] = acc
					updated = true
				}
			}
		}
	}
	if updated {
		data.LastUpdated = timestamp()
		writeJSON(s.sequenceFile, data)
	}
}

// --- Public commands ---

func (s *Switcher) AddAccount() error {
	s.setupDirs()
	s.initSequenceFile()
	s.migrateOrgFields()

	email, orgUUID, ok := s.currentAccount()
	if !ok {
		return fmt.Errorf("no active Claude account found. Please log in first")
	}

	if s.accountExists(email, orgUUID) {
		// Update existing
		data := s.getSequence()
		var accountNum string
		for num, acc := range data.Accounts {
			if acc.Email == email && acc.OrganizationUUID == orgUUID {
				accountNum = num
				break
			}
		}

		creds, err := s.readCredentials()
		if err != nil || creds == "" {
			return fmt.Errorf("failed to read credentials for current account")
		}

		configPath := s.claudeConfigPath()
		config, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("failed to read Claude config: %w", err)
		}

		s.writeAccountCreds(accountNum, email, creds)
		s.writeAccountConfig(accountNum, email, string(config))

		n, _ := strconv.Atoi(accountNum)
		data.ActiveAccountNumber = &n
		data.LastUpdated = timestamp()
		writeJSON(s.sequenceFile, data)

		tag := displayTag(data.Accounts[accountNum].OrganizationName, orgUUID)
		s.logger.Printf("Updated credentials for account %s: %s", accountNum, email)
		fmt.Printf("%s for Account %s (%s %s).\n",
			Accent("Updated credentials"), accountNum, email, Muted("["+tag+"]"))
		return nil
	}

	// New account
	accountNum := strconv.Itoa(s.nextAccountNumber())

	creds, err := s.readCredentials()
	if err != nil || creds == "" {
		return fmt.Errorf("failed to read credentials for current account")
	}

	configPath := s.claudeConfigPath()
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read Claude config: %w", err)
	}

	// Extract UUID and org info from config
	var cfgMap map[string]json.RawMessage
	json.Unmarshal(configBytes, &cfgMap)
	var oauth struct {
		AccountUUID string `json:"accountUuid"`
		OrgUUID     string `json:"organizationUuid"`
		OrgName     string `json:"organizationName"`
	}
	if raw, ok := cfgMap["oauthAccount"]; ok {
		json.Unmarshal(raw, &oauth)
	}

	s.writeAccountCreds(accountNum, email, creds)
	s.writeAccountConfig(accountNum, email, string(configBytes))

	data := s.getSequence()
	data.Accounts[accountNum] = AccountEntry{
		Email:            email,
		UUID:             oauth.AccountUUID,
		OrganizationUUID: oauth.OrgUUID,
		OrganizationName: oauth.OrgName,
		Added:            timestamp(),
	}
	n, _ := strconv.Atoi(accountNum)
	data.Sequence = append(data.Sequence, n)
	data.ActiveAccountNumber = &n
	data.LastUpdated = timestamp()

	writeJSON(s.sequenceFile, data)

	tag := displayTag(oauth.OrgName, oauth.OrgUUID)
	s.logger.Printf("Added account %s: %s (org: %s)", accountNum, email, oauth.OrgUUID)
	fmt.Printf("%s Account %s: %s %s\n", Accent("Added"), accountNum, email, Muted("["+tag+"]"))
	return nil
}

func (s *Switcher) RemoveAccount(identifier string) error {
	if _, err := os.Stat(s.sequenceFile); os.IsNotExist(err) {
		return fmt.Errorf("no accounts are managed yet")
	}
	s.migrateOrgFields()

	// Handle ambiguous email
	if _, err := strconv.Atoi(identifier); err != nil {
		if !validEmail(identifier) {
			return fmt.Errorf("invalid email format: %s", identifier)
		}
		data := s.getSequence()
		if data != nil {
			var matches []string
			for num, acc := range data.Accounts {
				if acc.Email == identifier {
					matches = append(matches, num)
				}
			}
			if len(matches) > 1 {
				fmt.Printf("Multiple accounts found for '%s':\n", identifier)
				for _, num := range matches {
					acc := data.Accounts[num]
					tag := displayTag(acc.OrganizationName, acc.OrganizationUUID)
					fmt.Printf("  %s: %s %s\n", num, identifier, Muted("["+tag+"]"))
				}
				fmt.Print("Enter account number to remove: ")
				reader := bufio.NewReader(os.Stdin)
				choice, _ := reader.ReadString('\n')
				choice = strings.TrimSpace(choice)
				found := false
				for _, m := range matches {
					if m == choice {
						found = true
					}
				}
				if !found {
					fmt.Println(Dimmed("Cancelled"))
					return nil
				}
				identifier = choice
			}
		}
	}

	accountNum, err := s.resolveIdentifier(identifier)
	if err != nil {
		return err
	}
	if accountNum == "" {
		return fmt.Errorf("no account found with identifier: %s", identifier)
	}

	data := s.getSequence()
	acc, exists := data.Accounts[accountNum]
	if !exists {
		return fmt.Errorf("Account-%s does not exist", accountNum)
	}

	if data.ActiveAccountNumber != nil && strconv.Itoa(*data.ActiveAccountNumber) == accountNum {
		PrintWarning(fmt.Sprintf("Warning: Account-%s (%s) is currently active", accountNum, acc.Email))
	}

	fmt.Printf("Are you sure you want to permanently remove Account-%s (%s)? [y/N] ", accountNum, acc.Email)
	reader := bufio.NewReader(os.Stdin)
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println(Dimmed("Cancelled"))
		return nil
	}

	s.deleteAccountCreds(accountNum, acc.Email)
	configFile := filepath.Join(s.configsDir, fmt.Sprintf(".claude-config-%s-%s.json", accountNum, acc.Email))
	os.Remove(configFile)

	delete(data.Accounts, accountNum)
	n, _ := strconv.Atoi(accountNum)
	newSeq := make([]int, 0, len(data.Sequence))
	for _, sn := range data.Sequence {
		if sn != n {
			newSeq = append(newSeq, sn)
		}
	}
	data.Sequence = newSeq
	data.LastUpdated = timestamp()
	writeJSON(s.sequenceFile, data)

	s.logger.Printf("Removed account %s: %s", accountNum, acc.Email)
	fmt.Printf("%s Account-%s (%s)\n", Accent("Removed"), accountNum, acc.Email)
	return nil
}

func (s *Switcher) ListAccounts() error {
	if _, err := os.Stat(s.sequenceFile); os.IsNotExist(err) {
		fmt.Println(Dimmed("No accounts are managed yet."))
		s.firstRunSetup()
		return nil
	}
	s.migrateOrgFields()

	data := s.getSequence()
	if data == nil {
		return fmt.Errorf("failed to read sequence data")
	}

	curEmail, curOrgUUID, _ := s.currentAccount()

	// Find active account
	var activeNum string
	for num, acc := range data.Accounts {
		if acc.Email == curEmail && acc.OrganizationUUID == curOrgUUID {
			activeNum = num
			break
		}
	}

	type accountInfo struct {
		num      int
		email    string
		orgName  string
		orgUUID  string
		isActive bool
		token    string
	}

	var accounts []accountInfo
	for _, seqNum := range data.Sequence {
		numStr := strconv.Itoa(seqNum)
		acc, ok := data.Accounts[numStr]
		if !ok {
			continue
		}
		isActive := numStr == activeNum

		var creds string
		if isActive {
			creds, _ = s.readCredentials()
		} else {
			creds = s.readAccountCreds(numStr, acc.Email)
		}
		token := extractAccessToken(creds)
		accounts = append(accounts, accountInfo{seqNum, acc.Email, acc.OrganizationName, acc.OrganizationUUID, isActive, token})
	}

	// Fetch usage in parallel
	type usageEntry struct {
		usage *usageResult
		err   string
	}
	usages := make([]usageEntry, len(accounts))
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Add(1)
		go func(idx int, token string) {
			defer wg.Done()
			if token == "" {
				usages[idx] = usageEntry{err: "no credentials"}
				return
			}
			usages[idx] = usageEntry{usage: fetchUsage(token)}
		}(i, acc.token)
	}
	wg.Wait()

	fmt.Println(Bolded("Accounts:"))
	for i, acc := range accounts {
		tag := displayTag(acc.orgName, acc.orgUUID)
		if acc.isActive {
			fmt.Printf("  %d: %s %s %s\n", acc.num, acc.email, Muted("["+tag+"]"), BoldAccent("(active)"))
		} else {
			fmt.Printf("  %d: %s %s\n", acc.num, acc.email, Muted("["+tag+"]"))
		}

		u := usages[i]
		if u.err != "" {
			fmt.Printf("     %s\n", Dimmed(u.err))
		} else if u.usage == nil {
			fmt.Printf("     %s\n", Dimmed("usage unavailable"))
		} else {
			var lines []string
			if u.usage.FiveHour != nil {
				h := u.usage.FiveHour
				if h.Clock != "" {
					lines = append(lines, fmt.Sprintf("5h: %3.0f%%   resets %-12s  in %s", h.Pct, h.Clock, h.Countdown))
				} else {
					lines = append(lines, fmt.Sprintf("5h: %3.0f%%", h.Pct))
				}
			}
			if u.usage.SevenDay != nil {
				d := u.usage.SevenDay
				if d.Clock != "" {
					lines = append(lines, fmt.Sprintf("7d: %3.0f%%   resets %-12s  in %s", d.Pct, d.Clock, d.Countdown))
				} else {
					lines = append(lines, fmt.Sprintf("7d: %3.0f%%", d.Pct))
				}
			}
			for j, line := range lines {
				connector := "├"
				if j == len(lines)-1 {
					connector = "└"
				}
				fmt.Printf("     %s %s\n", Dimmed(connector), Muted(line))
			}
		}

		if i < len(accounts)-1 {
			fmt.Println()
		}
	}
	return nil
}

func (s *Switcher) Status() error {
	email, orgUUID, ok := s.currentAccount()
	if !ok {
		fmt.Printf("%s %s\n", Bolded("Status:"), Dimmed("No active Claude account"))
		return nil
	}

	s.migrateOrgFields()
	data := s.getSequence()
	if data == nil {
		fmt.Printf("%s %s %s\n", Bolded("Status:"), email, Dimmed("(not managed)"))
		return nil
	}

	for num, acc := range data.Accounts {
		if acc.Email == email && acc.OrganizationUUID == orgUUID {
			tag := displayTag(acc.OrganizationName, orgUUID)
			total := len(data.Accounts)
			fmt.Printf("%s %s (%s %s)\n", Bolded("Status:"),
				Accent("Account-"+num), email, Muted("["+tag+"]"))
			fmt.Printf("  %s\n", Dimmed(fmt.Sprintf("Total managed accounts: %d", total)))
			return nil
		}
	}

	fmt.Printf("%s %s %s\n", Bolded("Status:"), email, Dimmed("(not managed)"))
	return nil
}

func (s *Switcher) Switch() error {
	if _, err := os.Stat(s.sequenceFile); os.IsNotExist(err) {
		return fmt.Errorf("no accounts are managed yet")
	}

	email, orgUUID, ok := s.currentAccount()
	if !ok {
		return fmt.Errorf("no active Claude account found")
	}
	s.migrateOrgFields()

	if !s.accountExists(email, orgUUID) {
		fmt.Printf("%s Active account '%s' was not managed.\n", Accent("Notice:"), email)
		s.AddAccount()
		data := s.getSequence()
		if data.ActiveAccountNumber != nil {
			fmt.Printf("It has been automatically added as Account-%d.\n", *data.ActiveAccountNumber)
		}
		fmt.Println(Dimmed("Please run the switch command again to switch to the next account."))
		return nil
	}

	data := s.getSequence()
	seq := data.Sequence
	if len(seq) < 2 {
		fmt.Println(Dimmed("Only one account is managed. Add more accounts to switch between."))
		return nil
	}

	active := 0
	if data.ActiveAccountNumber != nil {
		active = *data.ActiveAccountNumber
	}

	idx := 0
	for i, n := range seq {
		if n == active {
			idx = i
			break
		}
	}
	nextIdx := (idx + 1) % len(seq)
	nextAccount := strconv.Itoa(seq[nextIdx])

	return s.performSwitch(nextAccount)
}

func (s *Switcher) SwitchTo(identifier string) error {
	if _, err := os.Stat(s.sequenceFile); os.IsNotExist(err) {
		return fmt.Errorf("no accounts are managed yet")
	}
	s.migrateOrgFields()

	// Handle ambiguous email
	if _, err := strconv.Atoi(identifier); err != nil {
		if !validEmail(identifier) {
			return fmt.Errorf("invalid email format: %s", identifier)
		}
		data := s.getSequence()
		if data != nil {
			var matches []string
			for num, acc := range data.Accounts {
				if acc.Email == identifier {
					matches = append(matches, num)
				}
			}
			if len(matches) > 1 {
				sort.Strings(matches)
				fmt.Printf("Multiple accounts found for '%s':\n", identifier)
				for _, num := range matches {
					acc := data.Accounts[num]
					tag := displayTag(acc.OrganizationName, acc.OrganizationUUID)
					fmt.Printf("  %s: %s %s\n", num, identifier, Muted("["+tag+"]"))
				}
				fmt.Print("Enter account number to switch to: ")
				reader := bufio.NewReader(os.Stdin)
				choice, _ := reader.ReadString('\n')
				choice = strings.TrimSpace(choice)
				found := false
				for _, m := range matches {
					if m == choice {
						found = true
					}
				}
				if !found {
					fmt.Println(Dimmed("Cancelled"))
					return nil
				}
				identifier = choice
			}
		}
	}

	target, err := s.resolveIdentifier(identifier)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("no account found with identifier: %s", identifier)
	}

	data := s.getSequence()
	if _, exists := data.Accounts[target]; !exists {
		return fmt.Errorf("Account-%s does not exist", target)
	}

	return s.performSwitch(target)
}

func (s *Switcher) performSwitch(targetAccount string) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer lock.release()

	data := s.getSequence()
	currentAccount := "0"
	if data.ActiveAccountNumber != nil {
		currentAccount = strconv.Itoa(*data.ActiveAccountNumber)
	}
	targetEmail := data.Accounts[targetAccount].Email

	curEmail, _, ok := s.currentAccount()
	if !ok {
		return fmt.Errorf("no current account to switch from")
	}

	configPath := s.claudeConfigPath()
	origCreds, err := s.readCredentials()
	if err != nil {
		return fmt.Errorf("failed to read current credentials: %w", err)
	}
	origConfig, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read Claude config: %w", err)
	}

	// Track completed steps for rollback
	var completed []string
	rollback := func() {
		for i := len(completed) - 1; i >= 0; i-- {
			switch completed[i] {
			case "credentials_written":
				s.writeCredentials(origCreds)
			case "config_written":
				os.WriteFile(configPath, origConfig, 0600)
			case "sequence_updated":
				n, _ := strconv.Atoi(currentAccount)
				data.ActiveAccountNumber = &n
				data.LastUpdated = timestamp()
				writeJSON(s.sequenceFile, data)
			}
		}
	}

	// Step 1: Backup current
	s.writeAccountCreds(currentAccount, curEmail, origCreds)
	s.writeAccountConfig(currentAccount, curEmail, string(origConfig))
	s.logger.Printf("Backed up account %s", currentAccount)

	// Step 2: Read target
	targetCreds := s.readAccountCreds(targetAccount, targetEmail)
	targetConfig := s.readAccountConfig(targetAccount, targetEmail)
	if targetCreds == "" || targetConfig == "" {
		return fmt.Errorf("missing backup data for Account-%s", targetAccount)
	}

	// Step 3: Write target credentials
	if err := s.writeCredentials(targetCreds); err != nil {
		return fmt.Errorf("failed to write credentials: %w", err)
	}
	completed = append(completed, "credentials_written")

	// Step 4: Update config
	var targetCfgMap map[string]json.RawMessage
	if json.Unmarshal([]byte(targetConfig), &targetCfgMap) != nil {
		rollback()
		return fmt.Errorf("invalid target config")
	}
	oauthSection, ok := targetCfgMap["oauthAccount"]
	if !ok {
		rollback()
		return fmt.Errorf("invalid oauthAccount in backup")
	}

	var currentCfgMap map[string]json.RawMessage
	json.Unmarshal(origConfig, &currentCfgMap)
	currentCfgMap["oauthAccount"] = oauthSection
	newCfg, _ := json.MarshalIndent(currentCfgMap, "", "  ")
	if err := os.WriteFile(configPath, newCfg, 0600); err != nil {
		rollback()
		return fmt.Errorf("failed to write config: %w", err)
	}
	completed = append(completed, "config_written")

	// Step 5: Update sequence
	n, _ := strconv.Atoi(targetAccount)
	data.ActiveAccountNumber = &n
	data.LastUpdated = timestamp()
	if err := writeJSON(s.sequenceFile, data); err != nil {
		rollback()
		return fmt.Errorf("failed to update sequence: %w", err)
	}
	completed = append(completed, "sequence_updated")

	s.logger.Printf("Switched from account %s to %s", currentAccount, targetAccount)
	fmt.Printf("%s Account-%s (%s)\n", Accent("Switched to"), targetAccount, targetEmail)
	s.ListAccounts()
	fmt.Println()
	PrintWarning("Please restart Claude Code to use the new authentication.")
	fmt.Println()
	return nil
}

func (s *Switcher) Purge() error {
	PrintWarning("This will remove ALL claude-swap data from your system:")
	fmt.Printf("  - Backup directory: %s\n", s.backupDir)
	if s.platform == "linux" || s.platform == "wsl" {
		fmt.Println("  - All stored account credential files")
	} else {
		fmt.Println("  - All stored account credentials from the system keyring")
	}
	fmt.Println()
	fmt.Println(Dimmed("Note: This does NOT affect your current Claude Code login."))
	fmt.Println()

	fmt.Print("Are you sure you want to purge all data? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
		fmt.Println(Dimmed("Cancelled"))
		return nil
	}

	var removed []string

	data := s.getSequence()
	if data != nil {
		for num, acc := range data.Accounts {
			if s.platform == "linux" || s.platform == "wsl" {
				f := filepath.Join(s.credentialsDir, fmt.Sprintf(".creds-%s-%s.enc", num, acc.Email))
				if err := os.Remove(f); err == nil {
					removed = append(removed, "Credential file: "+filepath.Base(f))
				}
			} else {
				s.deleteAccountCreds(num, acc.Email)
				removed = append(removed, fmt.Sprintf("Credential: account-%s-%s", num, acc.Email))
			}
		}
	}

	if err := os.RemoveAll(s.backupDir); err == nil {
		removed = append(removed, "Directory: "+s.backupDir)
	}

	if len(removed) > 0 {
		fmt.Printf("\n%s\n", Accent("Removed:"))
		for _, item := range removed {
			fmt.Printf("  %s %s\n", Dimmed("-"), item)
		}
	} else {
		fmt.Printf("\n%s\n", Dimmed("No claude-swap data found to remove."))
	}
	fmt.Printf("\n%s\n", Accent("Purge complete."))
	return nil
}

func (s *Switcher) firstRunSetup() {
	email, _, ok := s.currentAccount()
	if !ok {
		fmt.Println(Dimmed("No active Claude account found. Please log in first."))
		return
	}

	fmt.Printf("No managed accounts found. Add current account (%s) to managed list? [Y/n] ", email)
	reader := bufio.NewReader(os.Stdin)
	resp, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(resp)) == "n" {
		fmt.Println(Dimmed("Setup cancelled. You can run 'cswap --add-account' later."))
		return
	}
	s.AddAccount()
}

// IsRoot checks if running as root outside container
func (s *Switcher) IsRoot() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return os.Geteuid() == 0 && !s.isContainer()
}
