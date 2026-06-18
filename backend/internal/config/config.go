package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

type Config struct {
	AppEnv                string
	HTTPAddr              string
	CORSAllowedOrigins    []string
	CORSAllowedOriginsRaw string
	JWTSecret             string
	TokenTTL              time.Duration
	LoginRateAttempts     int
	LoginRateAttemptsRaw  string
	LoginRateAttemptsOK   bool
	LoginRateWindow       time.Duration
	LoginRateWindowRaw    string
	LoginRateWindowOK     bool
	TerminalSessionTTL    time.Duration
	TerminalSessionTTLRaw string
	TerminalSessionTTLOK  bool
	DBDriver              string
	DatabaseURL           string
	DBConnectMaxAttempts  int
	DBConnectAttemptsRaw  string
	DBConnectAttemptsOK   bool
	DBConnectRetryDelay   time.Duration
	DBConnectDelayRaw     string
	DBConnectDelayOK      bool
	DeploymentConcurrency int
	DeploymentConcRaw     string
	DeploymentConcOK      bool
	BootServicesEnabled   bool
	BootServiceMode       string
	BootBindInterface     string
	BootDHCPListenAddr    string
	BootDHCPServerIP      string
	BootDHCPLeaseStart    string
	BootDHCPLeaseEnd      string
	BootTFTPListenAddr    string
	BootTFTPRoot          string
	BootTFTPBootfileUEFI  string
	BootTFTPBootfileBIOS  string
	RedisAddr             string
	RedisPassword         string
	RedisDB               int
	AdminEmail            string
	AdminPassword         string
	CredentialKey         string
	BMCAdapter            string
	CollectorMode         string
	BootBaseURL           string
	SSHOperationsMode     string
	SSHHostKeyPolicy      string
	SSHConnectTimeout     time.Duration
	SSHConnectTimeoutRaw  string
	SSHConnectTimeoutOK   bool
	ImageStorageDir       string
	ImageUploadMaxBytes   int64
	ImageUploadMaxMBRaw   string
	ImageUploadMaxMBValid bool
	MetadataRequireDeploy bool
	EnableDemoSeeder      bool
}

type Issue struct {
	Level   string `json:"level"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

type ValidationResult struct {
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}

func Load() Config {
	imageUploadMaxMB, imageUploadMaxMBRaw, imageUploadMaxMBValid := envIntWithValidity("IMAGE_UPLOAD_MAX_MB", 20)
	dbConnectAttempts, dbConnectAttemptsRaw, dbConnectAttemptsOK := envIntWithValidity("DB_CONNECT_MAX_ATTEMPTS", 12)
	dbConnectDelayMS, dbConnectDelayRaw, dbConnectDelayOK := envIntWithValidity("DB_CONNECT_RETRY_DELAY_MS", 1000)
	deploymentConcurrency, deploymentConcurrencyRaw, deploymentConcurrencyOK := envIntWithValidity("DEPLOYMENT_CONCURRENCY", 20)
	loginRateAttempts, loginRateAttemptsRaw, loginRateAttemptsOK := envIntWithValidity("LOGIN_RATE_LIMIT_ATTEMPTS", 10)
	loginRateWindowSeconds, loginRateWindowRaw, loginRateWindowOK := envIntWithValidity("LOGIN_RATE_LIMIT_WINDOW_SECONDS", 300)
	terminalSessionTTLMinutes, terminalSessionTTLRaw, terminalSessionTTLOK := envIntWithValidity("TERMINAL_SESSION_TTL_MINUTES", 60)
	sshConnectTimeoutSeconds, sshConnectTimeoutRaw, sshConnectTimeoutOK := envIntWithValidity("SSH_CONNECT_TIMEOUT_SECONDS", 10)
	appEnv := normalize(env("APP_ENV", "development"))
	corsOrigins, corsOriginsRaw := envList("CORS_ALLOWED_ORIGINS", DefaultCORSAllowedOrigins())
	return Config{
		AppEnv:                appEnv,
		HTTPAddr:              env("HTTP_ADDR", ":8080"),
		CORSAllowedOrigins:    corsOrigins,
		CORSAllowedOriginsRaw: corsOriginsRaw,
		JWTSecret:             env("JWT_SECRET", "change-me-in-production"),
		TokenTTL:              time.Duration(envInt("JWT_TTL_HOURS", 12)) * time.Hour,
		LoginRateAttempts:     loginRateAttempts,
		LoginRateAttemptsRaw:  loginRateAttemptsRaw,
		LoginRateAttemptsOK:   loginRateAttemptsOK,
		LoginRateWindow:       time.Duration(loginRateWindowSeconds) * time.Second,
		LoginRateWindowRaw:    loginRateWindowRaw,
		LoginRateWindowOK:     loginRateWindowOK,
		TerminalSessionTTL:    time.Duration(terminalSessionTTLMinutes) * time.Minute,
		TerminalSessionTTLRaw: terminalSessionTTLRaw,
		TerminalSessionTTLOK:  terminalSessionTTLOK,
		DBDriver:              normalize(env("DB_DRIVER", "sqlite")),
		DatabaseURL:           env("DATABASE_URL", "file:baremetal.db?cache=shared"),
		DBConnectMaxAttempts:  dbConnectAttempts,
		DBConnectAttemptsRaw:  dbConnectAttemptsRaw,
		DBConnectAttemptsOK:   dbConnectAttemptsOK,
		DBConnectRetryDelay:   time.Duration(dbConnectDelayMS) * time.Millisecond,
		DBConnectDelayRaw:     dbConnectDelayRaw,
		DBConnectDelayOK:      dbConnectDelayOK,
		DeploymentConcurrency: deploymentConcurrency,
		DeploymentConcRaw:     deploymentConcurrencyRaw,
		DeploymentConcOK:      deploymentConcurrencyOK,
		BootServicesEnabled:   envBool("BOOT_SERVICES_ENABLED", false),
		BootServiceMode:       normalize(env("BOOT_SERVICE_MODE", "proxy")),
		BootBindInterface:     env("BOOT_BIND_INTERFACE", ""),
		BootDHCPListenAddr:    env("BOOT_DHCP_LISTEN_ADDR", ":67"),
		BootDHCPServerIP:      env("BOOT_DHCP_SERVER_IP", ""),
		BootDHCPLeaseStart:    env("BOOT_DHCP_LEASE_START", ""),
		BootDHCPLeaseEnd:      env("BOOT_DHCP_LEASE_END", ""),
		BootTFTPListenAddr:    env("BOOT_TFTP_LISTEN_ADDR", ":69"),
		BootTFTPRoot:          env("BOOT_TFTP_ROOT", "data/tftp"),
		BootTFTPBootfileUEFI:  env("BOOT_TFTP_BOOTFILE_UEFI", "ipxe.efi"),
		BootTFTPBootfileBIOS:  env("BOOT_TFTP_BOOTFILE_BIOS", "undionly.kpxe"),
		RedisAddr:             env("REDIS_ADDR", ""),
		RedisPassword:         env("REDIS_PASSWORD", ""),
		RedisDB:               envInt("REDIS_DB", 0),
		AdminEmail:            env("ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:         env("ADMIN_PASSWORD", "Admin@123456"),
		CredentialKey:         env("CREDENTIAL_KEY", "dev-only-32-byte-credential-key!!"),
		BMCAdapter:            normalize(env("BMC_ADAPTER", "simulated")),
		CollectorMode:         normalize(env("COLLECTOR_MODE", "simulated")),
		BootBaseURL:           env("BOOT_BASE_URL", "http://localhost:8080"),
		SSHOperationsMode:     normalize(env("SSH_OPERATIONS_MODE", "simulated")),
		SSHHostKeyPolicy:      normalize(env("SSH_HOST_KEY_POLICY", "insecure_ignore")),
		SSHConnectTimeout:     time.Duration(sshConnectTimeoutSeconds) * time.Second,
		SSHConnectTimeoutRaw:  sshConnectTimeoutRaw,
		SSHConnectTimeoutOK:   sshConnectTimeoutOK,
		ImageStorageDir:       env("IMAGE_STORAGE_DIR", "data/images"),
		ImageUploadMaxBytes:   int64(imageUploadMaxMB) * 1024 * 1024,
		ImageUploadMaxMBRaw:   imageUploadMaxMBRaw,
		ImageUploadMaxMBValid: imageUploadMaxMBValid,
		MetadataRequireDeploy: envBool("METADATA_REQUIRE_DEPLOYMENT_NETWORK", IsProduction(appEnv)),
		EnableDemoSeeder:      envBool("ENABLE_DEMO_SEEDER", true),
	}
}

func Validate(cfg Config) ValidationResult {
	var result ValidationResult
	production := IsProduction(cfg.AppEnv)

	if !oneOf(normalize(cfg.AppEnv), "development", "test", "production") {
		result.addWarning("APP_ENV", fmt.Sprintf("APP_ENV %q is not recognized; use development, test, or production", cfg.AppEnv))
	}

	if production && strings.TrimSpace(cfg.CORSAllowedOriginsRaw) == "" {
		result.addWarning("CORS_ALLOWED_ORIGINS", "production should set CORS_ALLOWED_ORIGINS to the deployed frontend origin(s), or terminate API and frontend on the same origin behind a reverse proxy")
	}
	if len(cfg.CORSAllowedOrigins) == 0 {
		result.addEnvironmentIssue(production, "CORS_ALLOWED_ORIGINS", "CORS_ALLOWED_ORIGINS must contain at least one origin")
	}
	for _, origin := range cfg.CORSAllowedOrigins {
		if origin == "*" {
			if production {
				result.addWarning("CORS_ALLOWED_ORIGINS", "wildcard CORS origin is not recommended in production")
			}
			continue
		}
		if err := validateHTTPOrigin(origin); err != nil {
			result.addEnvironmentIssue(production, "CORS_ALLOWED_ORIGINS", err.Error())
		}
	}

	if isPlaceholder(cfg.JWTSecret, "change-me-in-production") || len(strings.TrimSpace(cfg.JWTSecret)) < 32 {
		result.addEnvironmentIssue(production, "JWT_SECRET", "JWT_SECRET must be changed from the development default and contain at least 32 characters")
	}
	if isPlaceholder(cfg.CredentialKey, "dev-only-32-byte-credential-key!!", "change-this-credential-encryption-key") || len(strings.TrimSpace(cfg.CredentialKey)) < 32 {
		result.addEnvironmentIssue(production, "CREDENTIAL_KEY", "CREDENTIAL_KEY must be changed from the development default and contain at least 32 characters")
	}
	if isPlaceholder(cfg.AdminPassword, "Admin@123456") {
		result.addEnvironmentIssue(production, "ADMIN_PASSWORD", "ADMIN_PASSWORD must be changed from the development default")
	}
	if cfg.LoginRateAttempts < 1 || (cfg.LoginRateAttemptsRaw != "" && !cfg.LoginRateAttemptsOK) {
		result.addEnvironmentIssue(production, "LOGIN_RATE_LIMIT_ATTEMPTS", "LOGIN_RATE_LIMIT_ATTEMPTS must be a positive integer")
	}
	if cfg.LoginRateWindow <= 0 || (cfg.LoginRateWindowRaw != "" && !cfg.LoginRateWindowOK) {
		result.addEnvironmentIssue(production, "LOGIN_RATE_LIMIT_WINDOW_SECONDS", "LOGIN_RATE_LIMIT_WINDOW_SECONDS must be a positive integer")
	}
	if cfg.TerminalSessionTTL <= 0 || (cfg.TerminalSessionTTLRaw != "" && !cfg.TerminalSessionTTLOK) {
		result.addEnvironmentIssue(production, "TERMINAL_SESSION_TTL_MINUTES", "TERMINAL_SESSION_TTL_MINUTES must be a positive integer")
	}

	switch normalize(cfg.DBDriver) {
	case "postgres":
	case "sqlite":
		if production {
			result.addError("DB_DRIVER", "production must use DB_DRIVER=postgres; sqlite is for development and tests")
		}
	default:
		result.addEnvironmentIssue(production, "DB_DRIVER", fmt.Sprintf("unsupported DB_DRIVER %q; use postgres or sqlite", cfg.DBDriver))
	}
	if cfg.DBConnectMaxAttempts < 1 || (cfg.DBConnectAttemptsRaw != "" && !cfg.DBConnectAttemptsOK) {
		result.addEnvironmentIssue(production, "DB_CONNECT_MAX_ATTEMPTS", "DB_CONNECT_MAX_ATTEMPTS must be a positive integer")
	}
	if cfg.DBConnectRetryDelay <= 0 || (cfg.DBConnectDelayRaw != "" && !cfg.DBConnectDelayOK) {
		result.addEnvironmentIssue(production, "DB_CONNECT_RETRY_DELAY_MS", "DB_CONNECT_RETRY_DELAY_MS must be a positive integer number of milliseconds")
	}
	if cfg.DeploymentConcurrency < 1 || (cfg.DeploymentConcRaw != "" && !cfg.DeploymentConcOK) {
		result.addEnvironmentIssue(production, "DEPLOYMENT_CONCURRENCY", "DEPLOYMENT_CONCURRENCY must be a positive integer")
	}
	if cfg.BootServicesEnabled {
		if !oneOf(normalize(cfg.BootServiceMode), "proxy", "builtin", "external") {
			result.addEnvironmentIssue(production, "BOOT_SERVICE_MODE", fmt.Sprintf("unsupported BOOT_SERVICE_MODE %q; use proxy, builtin, or external", cfg.BootServiceMode))
		}
		if strings.TrimSpace(cfg.BootBindInterface) == "" {
			result.addEnvironmentIssue(production, "BOOT_BIND_INTERFACE", "BOOT_BIND_INTERFACE must name the lab NIC or VLAN when BOOT_SERVICES_ENABLED=true")
		}
		if err := validateUDPListenAddr(cfg.BootTFTPListenAddr); err != nil {
			result.addEnvironmentIssue(production, "BOOT_TFTP_LISTEN_ADDR", err.Error())
		}
		if strings.TrimSpace(cfg.BootTFTPRoot) == "" {
			result.addEnvironmentIssue(production, "BOOT_TFTP_ROOT", "BOOT_TFTP_ROOT must not be empty when BOOT_SERVICES_ENABLED=true")
		}
		if strings.TrimSpace(cfg.BootTFTPBootfileUEFI) == "" || strings.TrimSpace(cfg.BootTFTPBootfileBIOS) == "" {
			result.addEnvironmentIssue(production, "BOOT_TFTP_BOOTFILE", "BOOT_TFTP_BOOTFILE_UEFI and BOOT_TFTP_BOOTFILE_BIOS must be set when BOOT_SERVICES_ENABLED=true")
		}
		if normalize(cfg.BootServiceMode) != "external" {
			if err := validateUDPListenAddr(cfg.BootDHCPListenAddr); err != nil {
				result.addEnvironmentIssue(production, "BOOT_DHCP_LISTEN_ADDR", err.Error())
			}
			if err := validateNonLocalIPv4(cfg.BootDHCPServerIP); err != nil {
				result.addEnvironmentIssue(production, "BOOT_DHCP_SERVER_IP", err.Error())
			}
		}
		if normalize(cfg.BootServiceMode) == "builtin" {
			if err := validateDHCPRange(cfg.BootDHCPLeaseStart, cfg.BootDHCPLeaseEnd); err != nil {
				result.addEnvironmentIssue(production, "BOOT_DHCP_LEASE_RANGE", err.Error())
			}
		}
	} else if production {
		result.addWarning("BOOT_SERVICES_ENABLED", "real PXE/DHCP/TFTP listeners are disabled; enable them only on an isolated deployment network")
	}

	if !oneOf(normalize(cfg.BMCAdapter), "simulated", "redfish", "ipmi") {
		result.addEnvironmentIssue(production, "BMC_ADAPTER", fmt.Sprintf("unsupported BMC_ADAPTER %q; use simulated, redfish, or ipmi", cfg.BMCAdapter))
	}
	if production && normalize(cfg.BMCAdapter) == "simulated" {
		result.addWarning("BMC_ADAPTER", "production should use BMC_ADAPTER=redfish or ipmi for physical hardware validation")
	}
	if !oneOf(normalize(cfg.CollectorMode), "simulated", "ssh") {
		result.addEnvironmentIssue(production, "COLLECTOR_MODE", fmt.Sprintf("unsupported COLLECTOR_MODE %q; use simulated or ssh", cfg.CollectorMode))
	}
	if !oneOf(normalize(cfg.SSHOperationsMode), "simulated", "ssh") {
		result.addEnvironmentIssue(production, "SSH_OPERATIONS_MODE", fmt.Sprintf("unsupported SSH_OPERATIONS_MODE %q; use simulated or ssh", cfg.SSHOperationsMode))
	}
	if !oneOf(normalize(cfg.SSHHostKeyPolicy), "insecure_ignore") {
		result.addEnvironmentIssue(production, "SSH_HOST_KEY_POLICY", "SSH_HOST_KEY_POLICY currently supports insecure_ignore")
	}
	if production && normalize(cfg.SSHHostKeyPolicy) == "insecure_ignore" && (normalize(cfg.CollectorMode) == "ssh" || normalize(cfg.SSHOperationsMode) == "ssh") {
		result.addWarning("SSH_HOST_KEY_POLICY", "SSH host key verification is disabled; restrict SSH access to a controlled lab network until known_hosts support is configured")
	}
	if cfg.SSHConnectTimeout <= 0 || (cfg.SSHConnectTimeoutRaw != "" && !cfg.SSHConnectTimeoutOK) {
		result.addEnvironmentIssue(production, "SSH_CONNECT_TIMEOUT_SECONDS", "SSH_CONNECT_TIMEOUT_SECONDS must be a positive integer")
	}
	if err := validateHTTPBaseURL(cfg.BootBaseURL); err != nil {
		result.addEnvironmentIssue(production, "BOOT_BASE_URL", err.Error())
	} else if production && isLocalOrUnspecifiedURLHost(cfg.BootBaseURL) {
		result.addError("BOOT_BASE_URL", "production BOOT_BASE_URL must not use localhost, loopback, or unspecified addresses")
	}
	if cfg.ImageUploadMaxBytes <= 0 || (cfg.ImageUploadMaxMBRaw != "" && !cfg.ImageUploadMaxMBValid) {
		result.addEnvironmentIssue(production, "IMAGE_UPLOAD_MAX_MB", "IMAGE_UPLOAD_MAX_MB must be a positive integer")
	}
	if production && !cfg.MetadataRequireDeploy {
		result.addError("METADATA_REQUIRE_DEPLOYMENT_NETWORK", "production must require Metadata API access from enabled deployment networks")
	}
	if production && cfg.EnableDemoSeeder {
		result.addError("ENABLE_DEMO_SEEDER", "production must disable demo seeding to avoid creating demo assets, images, alerts, and templates in a real environment")
	}
	if production && strings.TrimSpace(cfg.RedisAddr) == "" {
		result.addWarning("REDIS_ADDR", "REDIS_ADDR is recommended in production; readiness will report Redis as disabled")
	}
	return result
}

func (r ValidationResult) Issues() []Issue {
	issues := make([]Issue, 0, len(r.Errors)+len(r.Warnings))
	issues = append(issues, r.Errors...)
	issues = append(issues, r.Warnings...)
	return issues
}

func (r ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r ValidationResult) HasWarnings() bool {
	return len(r.Warnings) > 0
}

func CheckImageStorage(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("IMAGE_STORAGE_DIR must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create image storage directory: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("create image storage probe: %w", err)
	}
	probePath := probe.Name()
	if _, err := probe.WriteString("ok"); err != nil {
		_ = probe.Close()
		_ = os.Remove(probePath)
		return fmt.Errorf("write image storage probe: %w", err)
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("close image storage probe: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("remove image storage probe: %w", err)
	}
	return nil
}

func BootRuntimeIssues(cfg Config) []Issue {
	if !cfg.BootServicesEnabled {
		return []Issue{{Level: "warning", Key: "BOOT_SERVICES_ENABLED", Message: "real PXE/DHCP/TFTP listeners are disabled"}}
	}
	issues := []Issue{}
	add := func(level, key, message string) {
		issues = append(issues, Issue{Level: level, Key: key, Message: message})
	}
	if err := os.MkdirAll(cfg.BootTFTPRoot, 0o755); err != nil {
		add("error", "BOOT_TFTP_ROOT", fmt.Sprintf("create TFTP root: %v", err))
		return issues
	}
	probe, err := os.CreateTemp(cfg.BootTFTPRoot, ".write-probe-*")
	if err != nil {
		add("error", "BOOT_TFTP_ROOT", fmt.Sprintf("create TFTP root probe: %v", err))
		return issues
	}
	probePath := probe.Name()
	if _, err := probe.WriteString("ok"); err != nil {
		_ = probe.Close()
		_ = os.Remove(probePath)
		add("error", "BOOT_TFTP_ROOT", fmt.Sprintf("write TFTP root probe: %v", err))
		return issues
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		add("error", "BOOT_TFTP_ROOT", fmt.Sprintf("close TFTP root probe: %v", err))
		return issues
	}
	if err := os.Remove(probePath); err != nil {
		add("error", "BOOT_TFTP_ROOT", fmt.Sprintf("remove TFTP root probe: %v", err))
	}
	for _, bootfile := range []struct {
		key  string
		name string
	}{
		{"BOOT_TFTP_BOOTFILE_UEFI", cfg.BootTFTPBootfileUEFI},
		{"BOOT_TFTP_BOOTFILE_BIOS", cfg.BootTFTPBootfileBIOS},
	} {
		if strings.TrimSpace(bootfile.name) == "" {
			continue
		}
		path, err := safeJoin(cfg.BootTFTPRoot, bootfile.name)
		if err != nil {
			add("error", bootfile.key, err.Error())
			continue
		}
		if _, err := os.Stat(path); err != nil {
			add("warning", bootfile.key, fmt.Sprintf("bootloader %q is not present in BOOT_TFTP_ROOT; iPXE chain scripts will work only for clients that already have iPXE", bootfile.name))
		}
	}
	return issues
}

func ResolveImageStoragePath(storageDir string, rawPath string) (string, error) {
	storageDir = strings.TrimSpace(storageDir)
	rawPath = strings.TrimSpace(rawPath)
	if storageDir == "" {
		return "", errors.New("IMAGE_STORAGE_DIR must not be empty")
	}
	if rawPath == "" {
		return "", errors.New("file_path is required")
	}
	storageAbs, err := filepath.Abs(storageDir)
	if err != nil {
		return "", fmt.Errorf("resolve IMAGE_STORAGE_DIR: %w", err)
	}
	candidateAbs, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("resolve file_path: %w", err)
	}
	if !filepath.IsAbs(rawPath) && !pathWithinDir(storageAbs, candidateAbs) {
		candidateAbs, err = filepath.Abs(filepath.Join(storageAbs, rawPath))
		if err != nil {
			return "", fmt.Errorf("resolve file_path in IMAGE_STORAGE_DIR: %w", err)
		}
	}
	if !pathWithinDir(storageAbs, candidateAbs) {
		return "", errors.New("file_path must be inside IMAGE_STORAGE_DIR")
	}
	return filepath.Clean(candidateAbs), nil
}

func ResolveExistingImageStoragePath(storageDir string, rawPath string) (string, error) {
	storageDir = strings.TrimSpace(storageDir)
	rawPath = strings.TrimSpace(rawPath)
	if storageDir == "" {
		return "", errors.New("IMAGE_STORAGE_DIR must not be empty")
	}
	if rawPath == "" {
		return "", errors.New("file_path is required")
	}
	storageAbs, err := filepath.Abs(storageDir)
	if err != nil {
		return "", fmt.Errorf("resolve IMAGE_STORAGE_DIR: %w", err)
	}
	candidateAbs, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("resolve file_path: %w", err)
	}
	if !filepath.IsAbs(rawPath) && !pathWithinDir(storageAbs, candidateAbs) {
		candidateAbs, err = filepath.Abs(filepath.Join(storageAbs, rawPath))
		if err != nil {
			return "", fmt.Errorf("resolve file_path in IMAGE_STORAGE_DIR: %w", err)
		}
	}
	storageReal, err := filepath.EvalSymlinks(storageAbs)
	if err != nil {
		return "", fmt.Errorf("resolve IMAGE_STORAGE_DIR symlinks: %w", err)
	}
	pathReal, err := filepath.EvalSymlinks(candidateAbs)
	if err != nil {
		return "", fmt.Errorf("resolve image file symlinks: %w", err)
	}
	if !pathWithinDir(storageReal, pathReal) {
		return "", errors.New("file_path must resolve inside IMAGE_STORAGE_DIR")
	}
	return filepath.Clean(pathReal), nil
}

func pathWithinDir(dir string, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}

func IsProduction(appEnv string) bool {
	return normalize(appEnv) == "production"
}

func env(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func envIntWithValidity(key string, fallback int) (int, string, bool) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, "", true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, raw, false
	}
	return value, raw, true
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envList(key string, fallback []string) ([]string, string) {
	raw := os.Getenv(key)
	if raw == "" {
		return append([]string{}, fallback...), ""
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" || seen[value] {
			continue
		}
		values = append(values, value)
		seen[value] = true
	}
	return values, raw
}

func DefaultCORSAllowedOrigins() []string {
	return []string{"http://localhost:5173", "http://127.0.0.1:5173", "http://localhost:8081", "http://127.0.0.1:8081"}
}

func (r *ValidationResult) addEnvironmentIssue(production bool, key string, message string) {
	if production {
		r.addError(key, message)
		return
	}
	r.addWarning(key, message)
}

func (r *ValidationResult) addError(key string, message string) {
	r.Errors = append(r.Errors, Issue{Level: "error", Key: key, Message: message})
}

func (r *ValidationResult) addWarning(key string, message string) {
	r.Warnings = append(r.Warnings, Issue{Level: "warning", Key: key, Message: message})
}

func isPlaceholder(value string, placeholders ...string) bool {
	normalized := strings.TrimSpace(value)
	for _, placeholder := range placeholders {
		if normalized == placeholder {
			return true
		}
	}
	return false
}

func validateHTTPBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("BOOT_BASE_URL is invalid: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("BOOT_BASE_URL must use http or https")
	}
	if parsed.Host == "" {
		return errors.New("BOOT_BASE_URL must include a host")
	}
	return nil
}

func isLocalOrUnspecifiedURLHost(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

func validateHTTPOrigin(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS contains invalid origin %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS origin %q must use http or https", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS origin %q must include a host", raw)
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS origin %q must not include path, query, or fragment", raw)
	}
	return nil
}

func validateUDPListenAddr(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return errors.New("listen address must not be empty")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("listen address %q must be in host:port form: %w", value, err)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return fmt.Errorf("listen address %q must use a port between 1 and 65535", value)
	}
	if strings.TrimSpace(host) == "" {
		return nil
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return nil
	}
	if !hostnamePattern.MatchString(host) {
		return fmt.Errorf("listen address %q has an invalid host", value)
	}
	return nil
}

func validateNonLocalIPv4(raw string) error {
	value := strings.TrimSpace(raw)
	ip := net.ParseIP(value)
	if ip == nil || ip.To4() == nil {
		return errors.New("BOOT_DHCP_SERVER_IP must be an IPv4 address reachable by PXE clients")
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("BOOT_DHCP_SERVER_IP must not be localhost, unspecified, or multicast")
	}
	return nil
}

func validateDHCPRange(startRaw, endRaw string) error {
	start := net.ParseIP(strings.TrimSpace(startRaw)).To4()
	end := net.ParseIP(strings.TrimSpace(endRaw)).To4()
	if start == nil || end == nil {
		return errors.New("BOOT_DHCP_LEASE_START and BOOT_DHCP_LEASE_END must be IPv4 addresses for builtin DHCP")
	}
	if ipv4ToUint32(start) > ipv4ToUint32(end) {
		return errors.New("BOOT_DHCP_LEASE_START must be less than or equal to BOOT_DHCP_LEASE_END")
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	root = strings.TrimSpace(root)
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if root == "" || name == "" {
		return "", errors.New("path root and name are required")
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "../") || name == ".." {
		return "", errors.New("bootfile path must be relative to BOOT_TFTP_ROOT")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	if !pathWithinDir(rootAbs, pathAbs) {
		return "", errors.New("bootfile path must stay inside BOOT_TFTP_ROOT")
	}
	return pathAbs, nil
}

func ipv4ToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
