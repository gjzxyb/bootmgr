package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type RetryLogger func(format string, args ...any)

func Connect(cfg config.Config) (*gorm.DB, error) {
	return ConnectWithRetry(cfg, nil)
}

func ConnectWithRetry(cfg config.Config, logf RetryLogger) (*gorm.DB, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.DBDriver))
	if driver != "postgres" && driver != "sqlite" {
		return nil, fmt.Errorf("unsupported DB_DRIVER %q", cfg.DBDriver)
	}
	attempts := cfg.DBConnectMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := cfg.DBConnectRetryDelay
	if delay <= 0 {
		delay = time.Second
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		db, err := connectOnce(cfg, driver)
		if err == nil {
			if attempt > 1 && logf != nil {
				logf("database initialization succeeded on attempt %d/%d", attempt, attempts)
			}
			return db, nil
		}
		lastErr = err
		if attempt < attempts {
			if logf != nil {
				logf("database initialization attempt %d/%d failed: %v; retrying in %s", attempt, attempts, err, delay)
			}
			time.Sleep(delay)
		}
	}
	return nil, fmt.Errorf("database initialization failed after %d attempt(s): %w", attempts, lastErr)
}

func connectOnce(cfg config.Config, driver string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch driver {
	case "postgres":
		dialector = postgres.Open(cfg.DatabaseURL)
	case "sqlite":
		dialector = sqlite.Open(cfg.DatabaseURL)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(models.All()...); err != nil {
		return nil, err
	}
	if err := ensureIndexes(db); err != nil {
		return nil, err
	}
	if err := seed(db, cfg); err != nil {
		return nil, err
	}
	return db, nil
}

func ensureIndexes(db *gorm.DB) error {
	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_asset_no_identity ON servers (LOWER(asset_no)) WHERE asset_no <> '' AND deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_hostname_identity ON servers (LOWER(hostname)) WHERE hostname <> '' AND deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_servers_primary_mac_identity ON servers (LOWER(primary_mac)) WHERE primary_mac <> '' AND deleted_at IS NULL`,
	}
	for _, index := range indexes {
		if err := db.Exec(index).Error; err != nil {
			return fmt.Errorf("create server identity index: %w", err)
		}
	}
	return nil
}

func seed(db *gorm.DB, cfg config.Config) error {
	password, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := models.User{Email: cfg.AdminEmail, Name: "Platform Admin", Role: "admin", PasswordHash: string(password)}
	if err := db.Where("email = ?", cfg.AdminEmail).FirstOrCreate(&admin).Error; err != nil {
		return err
	}
	if !cfg.EnableDemoSeeder {
		return nil
	}

	now := time.Now().UTC()
	tenant := models.Tenant{TenantID: "default", Name: "Default Tenant", Status: "active", Owner: "ops", Description: "Demo tenant for MVP", Quota: datatypes.JSON([]byte(`{"servers":100}`))}
	if err := db.Where("tenant_id = ?", tenant.TenantID).FirstOrCreate(&tenant).Error; err != nil {
		return err
	}

	network := models.NetworkConfig{Name: "Demo deployment network", Purpose: "deployment", CIDR: "192.168.100.0/24", Gateway: "192.168.100.1", DNS: "192.168.100.1", VLANID: 100, DHCPMode: "proxy", ProxyDHCP: true, Status: "enabled", Description: "Demo deployment network for iPXE and metadata traffic", CreatedBy: cfg.AdminEmail, Options: datatypes.JSON([]byte(`{"mode":"mvp-simulated"}`))}
	if err := db.Where("name = ?", network.Name).FirstOrCreate(&network).Error; err != nil {
		return err
	}

	server := models.Server{
		AssetNo: "BM-0001", Hostname: "demo-node-01", Status: "ready", Architecture: "x86_64",
		SerialNumber: "SN-DEMO-0001", MotherboardUUID: "UUID-DEMO-0001", PrimaryMAC: "52:54:00:12:34:56",
		PrimaryIP: "192.168.100.21", TenantID: "default", Owner: "ops", Location: "DC-A", Rack: "R01", RackUnit: "U12",
		Tags: datatypes.JSON([]byte(`["mvp","demo"]`)), DiscoveredAt: &now,
	}
	if err := db.Where("asset_no = ?", server.AssetNo).FirstOrCreate(&server).Error; err != nil {
		return err
	}
	statusHistory := models.ServerStatusHistory{ServerID: server.ID, ToStatus: server.Status, Reason: "demo.seed", ActorEmail: cfg.AdminEmail}
	if err := db.Where("server_id = ? AND reason = ?", server.ID, statusHistory.Reason).FirstOrCreate(&statusHistory).Error; err != nil {
		return err
	}

	inv := models.HardwareInventory{ServerID: server.ID, CPUSummary: "2 x Intel Xeon Silver", MemorySummary: "128GB DDR4", DiskSummary: "2 x 960GB SSD", NetworkSummary: "2 x 10GbE", CollectedBy: "demo-seeder", CollectedAt: now, RawPayload: datatypes.JSON([]byte(`{"source":"demo"}`))}
	if err := db.Where("server_id = ?", server.ID).FirstOrCreate(&inv).Error; err != nil {
		return err
	}

	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Endpoint: "https://192.168.100.201", Username: "admin", Protocol: "https", Status: "ok", PowerState: "off", LastCheckedAt: &now}
	if err := db.Where("server_id = ?", server.ID).FirstOrCreate(&bmc).Error; err != nil {
		return err
	}

	demoImagePath, demoImageSHA, demoImageSize, err := ensureDemoImageFile(cfg.ImageStorageDir)
	if err != nil {
		return err
	}
	image := models.Image{Name: "Ubuntu Server 24.04", OSFamily: "ubuntu", OSVersion: "24.04", Architecture: "x86_64", FilePath: demoImagePath, SizeBytes: demoImageSize, SHA256: demoImageSHA, Status: "enabled", TestStatus: "tested_passed", Tags: datatypes.JSON([]byte(`["linux","demo"]`)), CreatedBy: cfg.AdminEmail}
	if err := db.Where("name = ?", image.Name).Assign(image).FirstOrCreate(&image).Error; err != nil {
		return err
	}

	if err := seedInstallTemplates(db, cfg.AdminEmail); err != nil {
		return err
	}

	workflowTemplate := models.WorkflowTemplate{Name: "Standard Linux Install", Version: "v1", Description: "MVP template-driven Linux installation workflow", Status: "enabled", CreatedBy: cfg.AdminEmail, Definition: datatypes.JSON([]byte(`{"steps":[{"name":"Hardware discovery","action":"discover_hardware"},{"name":"Render iPXE","action":"render_ipxe"},{"name":"Install OS","action":"install_os"},{"name":"Post install callback","action":"register_host"}]}`))}
	if err := db.Where("name = ? AND version = ?", workflowTemplate.Name, workflowTemplate.Version).FirstOrCreate(&workflowTemplate).Error; err != nil {
		return err
	}

	alert := models.Alert{ServerID: server.ID, RuleID: "disk.smart.warning", Severity: "warning", Status: "firing", Title: "Demo SMART warning", Description: "Demo alert for the MVP dashboard", TriggeredAt: now}
	if err := db.Where("rule_id = ? AND server_id = ?", alert.RuleID, server.ID).FirstOrCreate(&alert).Error; err != nil {
		return err
	}

	alertRules := []models.AlertRule{
		{RuleID: "cpu.high", Name: "CPU usage high", Description: "Fire when latest CPU usage is above threshold", MetricName: "cpu_usage", Operator: ">", Threshold: 30, Severity: "warning", Status: "enabled", CreatedBy: cfg.AdminEmail},
		{RuleID: "memory.high", Name: "Memory usage high", Description: "Fire when latest memory usage is above threshold", MetricName: "memory_usage", Operator: ">", Threshold: 85, Severity: "warning", Status: "enabled", CreatedBy: cfg.AdminEmail},
		{RuleID: "disk.full", Name: "Disk usage high", Description: "Fire when latest root filesystem usage is above threshold", MetricName: "disk_usage", Operator: ">", Threshold: 90, Severity: "critical", Status: "enabled", CreatedBy: cfg.AdminEmail},
		{RuleID: "disk.smart.warning", Name: "Disk SMART warning", Description: "Fire when latest SMART health sample reports an abnormal disk", MetricName: "disk_smart_health", Operator: ">", Threshold: 0, Severity: "warning", Status: "enabled", CreatedBy: cfg.AdminEmail},
		{RuleID: "host.offline", Name: "Host offline", Description: "Fire when latest host_up sample is 0", MetricName: "host_up", Operator: "==", Threshold: 0, Severity: "critical", Status: "enabled", CreatedBy: cfg.AdminEmail},
	}
	for _, alertRule := range alertRules {
		if err := db.Where("rule_id = ?", alertRule.RuleID).FirstOrCreate(&alertRule).Error; err != nil {
			return err
		}
	}

	logEvent := models.LogEvent{ServerID: server.ID, Source: "agentless", Level: "warning", Message: "Demo kernel disk latency spike detected", TraceID: "demo-log-0001", OccurredAt: now}
	return db.Where("trace_id = ?", logEvent.TraceID).FirstOrCreate(&logEvent).Error
}

func seedInstallTemplates(db *gorm.DB, adminEmail string) error {
	templates := []models.InstallTemplate{
		{
			Name:         "Ubuntu Server 24.04 Autoinstall",
			OSFamily:     "ubuntu",
			OSVersion:    "24.04",
			TemplateType: "cloud-init",
			Content: `#cloud-config
autoinstall:
  version: 1
  identity:
    hostname: {{hostname}}
    username: ubuntu
    password: "$6$replace-me"
  keyboard:
    layout: us
  locale: en_US.UTF-8
  ssh:
    install-server: true
  late-commands:
    - curtin in-target -- sh -c "mkdir -p /etc/baremetal-platform && echo metadata_token={{metadata_token}} > /etc/baremetal-platform/install.env"
`,
			VariablesSchema: datatypes.JSON([]byte(`{"hostname":"string","primary_ip":"string","metadata_token":"string"}`)),
			Version:         "v1",
			Status:          "enabled",
			CreatedBy:       adminEmail,
		},
		{
			Name:         "Rocky Linux 9 Kickstart",
			OSFamily:     "rocky",
			OSVersion:    "9",
			TemplateType: "kickstart",
			Content: `#version=RHEL9
text
network --bootproto=dhcp --hostname={{hostname}}
url --url="https://download.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os/"
keyboard us
lang en_US.UTF-8
timezone UTC --utc
rootpw --iscrypted $6$replace-me
firewall --enabled --service=ssh
selinux --enforcing
bootloader --location=mbr
clearpart --all --initlabel
autopart
reboot
%packages
@^minimal-environment
openssh-server
%end
%post
mkdir -p /etc/baremetal-platform
echo metadata_token={{metadata_token}} > /etc/baremetal-platform/install.env
%end
`,
			VariablesSchema: datatypes.JSON([]byte(`{"hostname":"string","primary_ip":"string","metadata_token":"string"}`)),
			Version:         "v1",
			Status:          "enabled",
			CreatedBy:       adminEmail,
		},
		{
			Name:         "Debian 12 Preseed",
			OSFamily:     "debian",
			OSVersion:    "12",
			TemplateType: "preseed",
			Content: `d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string {{hostname}}
d-i passwd/root-login boolean false
d-i passwd/user-fullname string Platform Admin
d-i passwd/username string platform
d-i passwd/user-password-crypted password $6$replace-me
d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
tasksel tasksel/first multiselect standard, ssh-server
d-i preseed/late_command string in-target sh -c 'mkdir -p /etc/baremetal-platform && echo metadata_token={{metadata_token}} > /etc/baremetal-platform/install.env'
d-i finish-install/reboot_in_progress note
`,
			VariablesSchema: datatypes.JSON([]byte(`{"hostname":"string","primary_ip":"string","metadata_token":"string"}`)),
			Version:         "v1",
			Status:          "enabled",
			CreatedBy:       adminEmail,
		},
	}
	for _, tmpl := range templates {
		template := tmpl
		if err := db.Where("name = ? AND version = ?", template.Name, template.Version).Assign(template).FirstOrCreate(&template).Error; err != nil {
			return err
		}
	}
	return nil
}

func FirstErr(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func ensureDemoImageFile(storageDir string) (string, string, int64, error) {
	if strings.TrimSpace(storageDir) == "" {
		storageDir = filepath.Join("data", "images")
	}
	path := filepath.Join(storageDir, "demo-ubuntu-24.04.iso")
	content := []byte("baremetal-platform-demo-image\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", 0, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", "", 0, err
		}
	} else if err != nil {
		return "", "", 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	sum := sha256.Sum256(data)
	return path, hex.EncodeToString(sum[:]), int64(len(data)), nil
}
