package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strconv"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSSHCommandExecutorUsesStoredPrivateKey(t *testing.T) {
	db := newCredentialTestDB(t)
	cfg := config.Config{CredentialKey: "collector-test-credential-key", SSHHostKeyPolicy: "insecure_ignore", SSHConnectTimeout: 10 * time.Second}
	credentials := NewCredentialService(db, cfg)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}))
	cred, err := credentials.Store("test-ssh", "ssh", "root", privateKeyPEM, "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}

	methods, err := (SSHCommandExecutor{db: db, credentials: credentials, hostKeyPolicy: "insecure_ignore", connectTimeout: time.Second}).authMethods(models.SSHAccess{
		AuthType:      "private_key",
		CredentialRef: strconv.FormatUint(uint64(cred.ID), 10),
	})
	if err != nil {
		t.Fatalf("build ssh auth methods: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("expected one public key auth method, got %d", len(methods))
	}
}

func TestSSHCommandExecutorSupportsStoredPasswordCredential(t *testing.T) {
	db := newCredentialTestDB(t)
	cfg := config.Config{CredentialKey: "collector-test-credential-key", SSHHostKeyPolicy: "insecure_ignore", SSHConnectTimeout: 10 * time.Second}
	credentials := NewCredentialService(db, cfg)
	cred, err := credentials.Store("test-ssh-password", "ssh", "root", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}

	methods, err := (SSHCommandExecutor{db: db, credentials: credentials, hostKeyPolicy: "insecure_ignore", connectTimeout: time.Second}).authMethods(models.SSHAccess{
		AuthType:      "password",
		CredentialRef: strconv.FormatUint(uint64(cred.ID), 10),
	})
	if err != nil {
		t.Fatalf("expected password credential to be supported, got %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("expected password and keyboard-interactive auth methods, got %d", len(methods))
	}
}

func TestParseMetricLinesAssignsUnits(t *testing.T) {
	samples := parseMetricLines("host_up=1\ndisk_smart_health=1\nnetwork_rx_mbps=12.5\ncpu_usage=88\n", time.Now().UTC())
	units := map[string]string{}
	for _, sample := range samples {
		units[sample.MetricName] = sample.Unit
	}
	if units["host_up"] != "bool" || units["disk_smart_health"] != "bool" {
		t.Fatalf("expected boolean units, got %#v", units)
	}
	if units["network_rx_mbps"] != "Mbps" {
		t.Fatalf("expected network unit Mbps, got %#v", units)
	}
	if units["cpu_usage"] != "%" {
		t.Fatalf("expected percentage unit, got %#v", units)
	}
}

func newCredentialTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Credential{}); err != nil {
		t.Fatalf("migrate credential: %v", err)
	}
	return db
}
