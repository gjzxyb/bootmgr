package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"
)

const testKnownHostsPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestValidateDevelopmentAllowsDemoDefaultsWithWarnings(t *testing.T) {
	result := Validate(baseTestConfig())
	if result.HasErrors() {
		t.Fatalf("development config should not block startup: %#v", result.Errors)
	}
	for _, key := range []string{"JWT_SECRET", "CREDENTIAL_KEY", "ADMIN_PASSWORD"} {
		if !hasIssue(result.Warnings, key) {
			t.Fatalf("expected warning for %s in %#v", key, result.Warnings)
		}
	}
}

func TestValidateProductionBlocksUnsafeDefaults(t *testing.T) {
	cfg := baseTestConfig()
	cfg.AppEnv = "production"
	cfg.BMCAdapter = "bad-adapter"

	result := Validate(cfg)
	for _, key := range []string{"JWT_SECRET", "CREDENTIAL_KEY", "ADMIN_PASSWORD", "DB_DRIVER", "BMC_ADAPTER", "BOOT_BASE_URL", "METADATA_REQUIRE_DEPLOYMENT_NETWORK", "ENABLE_DEMO_SEEDER"} {
		if !hasIssue(result.Errors, key) {
			t.Fatalf("expected production error for %s in %#v", key, result.Errors)
		}
	}
}

func TestValidateProductionAcceptsSafeConfig(t *testing.T) {
	cfg := safeProductionTestConfig()

	result := Validate(cfg)
	if result.HasErrors() {
		t.Fatalf("safe production config should not have blocking errors: %#v", result)
	}
}

func TestValidateProductionAcceptsKnownHostsSSHPolicy(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, []byte("example.com "+testKnownHostsPublicKey+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	cfg := safeProductionTestConfig()
	cfg.CollectorMode = "ssh"
	cfg.SSHOperationsMode = "ssh"
	cfg.SSHHostKeyPolicy = "known_hosts"
	cfg.SSHKnownHostsPath = knownHostsPath

	result := Validate(cfg)
	if result.HasErrors() {
		t.Fatalf("known_hosts SSH policy should be accepted in production: %#v", result)
	}
}

func TestValidateProductionBlocksInsecureSSHHostKeyPolicyWhenSSHEnabled(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.CollectorMode = "ssh"
	cfg.SSHOperationsMode = "ssh"
	cfg.SSHHostKeyPolicy = "insecure_ignore"

	result := Validate(cfg)
	if !hasIssue(result.Errors, "SSH_HOST_KEY_POLICY") {
		t.Fatalf("expected production error for insecure SSH host key policy with SSH enabled: %#v", result.Errors)
	}
}

func TestValidateProductionBlocksInsecureRedfishTLS(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.BMCAdapter = "redfish"
	cfg.RedfishInsecureTLS = true

	result := Validate(cfg)
	if !hasIssue(result.Errors, "REDFISH_INSECURE_TLS") {
		t.Fatalf("expected production error for insecure Redfish TLS: %#v", result.Errors)
	}
}

func TestValidateProductionBlocksInvalidRedfishCAPath(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.BMCAdapter = "redfish"
	cfg.RedfishCACertPath = filepath.Join(t.TempDir(), "missing-ca.pem")

	result := Validate(cfg)
	if !hasIssue(result.Errors, "REDFISH_CA_CERT_PATH") {
		t.Fatalf("expected production error for unreadable Redfish CA path: %#v", result.Errors)
	}
}

func TestValidateProductionBlocksUnscopedPXEListenerAddresses(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.BootDHCPListenAddr = ":67"
	cfg.BootTFTPListenAddr = ":69"

	result := Validate(cfg)
	for _, key := range []string{"BOOT_DHCP_LISTEN_ADDR", "BOOT_TFTP_LISTEN_ADDR"} {
		if !hasIssue(result.Errors, key) {
			t.Fatalf("expected production error for %s in %#v", key, result.Errors)
		}
	}
}

func TestValidateProductionBlocksUnreachablePXEListenerHosts(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.BootDHCPListenAddr = "0.0.0.0:67"
	cfg.BootTFTPListenAddr = "127.0.0.1:69"

	result := Validate(cfg)
	for _, key := range []string{"BOOT_DHCP_LISTEN_ADDR", "BOOT_TFTP_LISTEN_ADDR"} {
		if !hasIssue(result.Errors, key) {
			t.Fatalf("expected production error for %s in %#v", key, result.Errors)
		}
	}
}

func TestValidateProductionBlocksKnownHostsWithoutPath(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.CollectorMode = "ssh"
	cfg.SSHOperationsMode = "ssh"
	cfg.SSHHostKeyPolicy = "known_hosts"
	cfg.SSHKnownHostsPath = ""

	result := Validate(cfg)
	if !hasIssue(result.Errors, "SSH_KNOWN_HOSTS_PATH") {
		t.Fatalf("expected production error for missing SSH_KNOWN_HOSTS_PATH: %#v", result.Errors)
	}
}

func TestValidateProductionBlocksEmptyKnownHostsFile(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, []byte("# no hosts yet\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	cfg := safeProductionTestConfig()
	cfg.CollectorMode = "ssh"
	cfg.SSHOperationsMode = "ssh"
	cfg.SSHHostKeyPolicy = "known_hosts"
	cfg.SSHKnownHostsPath = knownHostsPath

	result := Validate(cfg)
	if !hasIssue(result.Errors, "SSH_KNOWN_HOSTS_PATH") {
		t.Fatalf("expected production error for empty SSH_KNOWN_HOSTS_PATH: %#v", result.Errors)
	}
}

func TestCheckKnownHostsCoverageReportsMissingTargets(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	knownHosts := strings.Join([]string{
		"node-a.example.com " + testKnownHostsPublicKey,
		"[node-b.example.com]:2222 " + testKnownHostsPublicKey,
		"*.lab.example.com " + testKnownHostsPublicKey,
		"",
	}, "\n")
	if err := os.WriteFile(knownHostsPath, []byte(knownHosts), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	coverage, err := CheckKnownHostsCoverage(knownHostsPath, []KnownHostsTarget{
		{Host: "node-a.example.com", Port: 22},
		{Host: "node-b.example.com", Port: 2222},
		{Host: "rack-1.lab.example.com", Port: 22},
		{Host: "missing.example.com", Port: 22},
	})
	if err != nil {
		t.Fatalf("check known_hosts coverage: %v", err)
	}
	if coverage.Total != 4 || coverage.Matched != 3 || len(coverage.Missing) != 1 || coverage.Missing[0].Host != "missing.example.com" {
		t.Fatalf("unexpected known_hosts coverage: %#v", coverage)
	}
}

func TestCheckKnownHostsCoverageMatchesHashedEntries(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	knownHosts := strings.Join([]string{
		knownhosts.HashHostname("hashed.example.com") + " " + testKnownHostsPublicKey,
		knownhosts.HashHostname("[hashed-port.example.com]:2222") + " " + testKnownHostsPublicKey,
		"",
	}, "\n")
	if err := os.WriteFile(knownHostsPath, []byte(knownHosts), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	coverage, err := CheckKnownHostsCoverage(knownHostsPath, []KnownHostsTarget{
		{Host: "hashed.example.com", Port: 22},
		{Host: "hashed-port.example.com", Port: 2222},
	})
	if err != nil {
		t.Fatalf("check known_hosts coverage: %v", err)
	}
	if coverage.Total != 2 || coverage.Matched != 2 || coverage.HashedEntries != 2 || len(coverage.Missing) != 0 {
		t.Fatalf("expected hashed entries to statically cover SSH targets, got %#v", coverage)
	}
}

func TestValidateProductionBlocksInvalidDatabaseRetryConfig(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.DBConnectMaxAttempts = 0
	cfg.DBConnectRetryDelay = 0

	result := Validate(cfg)
	for _, key := range []string{"DB_CONNECT_MAX_ATTEMPTS", "DB_CONNECT_RETRY_DELAY_MS"} {
		if !hasIssue(result.Errors, key) {
			t.Fatalf("expected production error for %s in %#v", key, result.Errors)
		}
	}
}

func TestValidateProductionBlocksPlaceholderDatabaseURL(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.DatabaseURL = "host=127.0.0.1 user=baremetal password=REPLACE_ME dbname=baremetal"

	result := Validate(cfg)
	if !hasIssue(result.Errors, "DATABASE_URL") {
		t.Fatalf("expected production error for placeholder DATABASE_URL: %#v", result.Errors)
	}
}

func TestValidateProductionBlocksInvalidDeploymentConcurrency(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.DeploymentConcurrency = 0

	result := Validate(cfg)
	if !hasIssue(result.Errors, "DEPLOYMENT_CONCURRENCY") {
		t.Fatalf("expected production error for DEPLOYMENT_CONCURRENCY in %#v", result.Errors)
	}
}

func TestValidateProductionBlocksInvalidLoginRateLimitConfig(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.LoginRateAttempts = 0
	cfg.LoginRateWindow = 0

	result := Validate(cfg)
	for _, key := range []string{"LOGIN_RATE_LIMIT_ATTEMPTS", "LOGIN_RATE_LIMIT_WINDOW_SECONDS"} {
		if !hasIssue(result.Errors, key) {
			t.Fatalf("expected production error for %s in %#v", key, result.Errors)
		}
	}
}

func TestValidateProductionBlocksInvalidTerminalTTLConfig(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.TerminalSessionTTL = 0

	result := Validate(cfg)
	if !hasIssue(result.Errors, "TERMINAL_SESSION_TTL_MINUTES") {
		t.Fatalf("expected production error for TERMINAL_SESSION_TTL_MINUTES in %#v", result.Errors)
	}
}

func TestValidateProductionBlocksInvalidCORSOrigin(t *testing.T) {
	cfg := safeProductionTestConfig()
	cfg.CORSAllowedOrigins = []string{"https://console.example.com/app"}
	cfg.CORSAllowedOriginsRaw = "https://console.example.com/app"

	result := Validate(cfg)
	if !hasIssue(result.Errors, "CORS_ALLOWED_ORIGINS") {
		t.Fatalf("expected production error for invalid CORS origin: %#v", result.Errors)
	}
}

func TestResolveImageStoragePathConstrainsToStorageDir(t *testing.T) {
	storageDir := t.TempDir()
	inside, err := ResolveImageStoragePath(storageDir, "ubuntu.iso")
	if err != nil {
		t.Fatalf("resolve relative image path: %v", err)
	}
	if filepath.Dir(inside) != storageDir {
		t.Fatalf("expected relative image path inside storage dir, got %q", inside)
	}

	nested := filepath.Join(storageDir, "nested", "rocky.iso")
	resolvedNested, err := ResolveImageStoragePath(storageDir, nested)
	if err != nil {
		t.Fatalf("resolve absolute image path: %v", err)
	}
	if resolvedNested != filepath.Clean(nested) {
		t.Fatalf("expected absolute image path to be preserved, got %q", resolvedNested)
	}

	outside := filepath.Join(t.TempDir(), "secret.iso")
	if _, err := ResolveImageStoragePath(storageDir, outside); err == nil {
		t.Fatalf("expected outside image path to be rejected")
	}
	if _, err := ResolveImageStoragePath(storageDir, ".."+string(os.PathSeparator)+"secret.iso"); err == nil {
		t.Fatalf("expected traversal image path to be rejected")
	}

	imagePath := filepath.Join(storageDir, "verified.iso")
	if err := os.WriteFile(imagePath, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	existing, err := ResolveExistingImageStoragePath(storageDir, imagePath)
	if err != nil {
		t.Fatalf("resolve existing image path: %v", err)
	}
	if existing == "" {
		t.Fatalf("expected existing image path")
	}
}

func safeProductionTestConfig() Config {
	cfg := baseTestConfig()
	cfg.AppEnv = "production"
	cfg.JWTSecret = strings.Repeat("j", 32)
	cfg.CredentialKey = strings.Repeat("c", 32)
	cfg.AdminPassword = "Admin@987654321"
	cfg.DBDriver = "postgres"
	cfg.DatabaseURL = "host=127.0.0.1 user=baremetal password=strong-db-password dbname=baremetal port=5432 sslmode=disable"
	cfg.RedisAddr = "redis:6379"
	cfg.BMCAdapter = "redfish"
	cfg.RedfishInsecureTLS = false
	cfg.CollectorMode = "simulated"
	cfg.SSHOperationsMode = "simulated"
	cfg.SSHHostKeyPolicy = "insecure_ignore"
	cfg.SSHConnectTimeout = 10 * time.Second
	cfg.BootBaseURL = "https://boot.example.com"
	cfg.BootServicesEnabled = true
	cfg.BootServiceMode = "proxy"
	cfg.BootBindInterface = "pxe-lab-vlan"
	cfg.BootDHCPListenAddr = "192.168.100.10:67"
	cfg.BootDHCPServerIP = "192.168.100.10"
	cfg.BootTFTPListenAddr = "192.168.100.10:69"
	cfg.BootTFTPRoot = "data/tftp"
	cfg.BootTFTPBootfileUEFI = "ipxe.efi"
	cfg.BootTFTPBootfileBIOS = "undionly.kpxe"
	cfg.ImageUploadMaxBytes = 100 * 1024 * 1024
	cfg.MetadataRequireDeploy = true
	cfg.DBConnectMaxAttempts = 6
	cfg.DBConnectRetryDelay = 500 * time.Millisecond
	cfg.DeploymentConcurrency = 20
	cfg.LoginRateAttempts = 10
	cfg.LoginRateWindow = 5 * time.Minute
	cfg.TerminalSessionTTL = time.Hour
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}
	cfg.CORSAllowedOriginsRaw = "https://console.example.com"
	cfg.EnableDemoSeeder = false
	return cfg
}

func baseTestConfig() Config {
	return Config{
		AppEnv:                "development",
		HTTPAddr:              ":8080",
		CORSAllowedOrigins:    DefaultCORSAllowedOrigins(),
		JWTSecret:             "change-me-in-production",
		DBDriver:              "sqlite",
		DatabaseURL:           "file:baremetal.db?cache=shared",
		AdminEmail:            "admin@example.com",
		AdminPassword:         "Admin@123456",
		CredentialKey:         "dev-only-32-byte-credential-key!!",
		BMCAdapter:            "simulated",
		RedfishInsecureTLS:    true,
		CollectorMode:         "simulated",
		SSHOperationsMode:     "simulated",
		SSHHostKeyPolicy:      "insecure_ignore",
		SSHConnectTimeout:     10 * time.Second,
		BootBaseURL:           "http://localhost:8080",
		ImageStorageDir:       "data/images",
		ImageUploadMaxBytes:   20 * 1024 * 1024,
		EnableDemoSeeder:      true,
		DBConnectMaxAttempts:  12,
		DBConnectRetryDelay:   time.Second,
		DeploymentConcurrency: 20,
		LoginRateAttempts:     10,
		LoginRateWindow:       5 * time.Minute,
		TerminalSessionTTL:    time.Hour,
	}
}

func hasIssue(issues []Issue, key string) bool {
	for _, issue := range issues {
		if issue.Key == key {
			return true
		}
	}
	return false
}
