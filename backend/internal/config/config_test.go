package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	cfg.RedisAddr = "redis:6379"
	cfg.BMCAdapter = "redfish"
	cfg.CollectorMode = "ssh"
	cfg.SSHOperationsMode = "ssh"
	cfg.SSHHostKeyPolicy = "insecure_ignore"
	cfg.SSHConnectTimeout = 10 * time.Second
	cfg.BootBaseURL = "https://boot.example.com"
	cfg.BootServicesEnabled = true
	cfg.BootServiceMode = "proxy"
	cfg.BootBindInterface = "pxe-lab-vlan"
	cfg.BootDHCPListenAddr = ":67"
	cfg.BootDHCPServerIP = "192.168.100.10"
	cfg.BootTFTPListenAddr = ":69"
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
