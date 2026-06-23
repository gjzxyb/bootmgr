package services

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestTerminalServiceUsesRealSSHMode(t *testing.T) {
	addr, _, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	db := newTerminalTestDB(t)
	cfg := config.Config{
		CredentialKey:      "terminal-ssh-test-key",
		SSHOperationsMode:  "ssh",
		SSHHostKeyPolicy:   "insecure_ignore",
		SSHConnectTimeout:  5 * time.Second,
		TerminalSessionTTL: time.Hour,
	}
	credentials := NewCredentialService(db, cfg)
	cred, err := credentials.Store("terminal-ssh-password", "ssh", "tester", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split ssh listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse ssh listener port: %v", err)
	}
	server := models.Server{AssetNo: "BM-TERMINAL-SSH", Hostname: "terminal-ssh", Status: "running", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	access := models.SSHAccess{
		ServerID:      server.ID,
		Host:          host,
		Port:          port,
		Username:      "tester",
		AuthType:      "password",
		CredentialRef: strconv.FormatUint(uint64(cred.ID), 10),
		Status:        "configured",
	}
	if err := db.Create(&access).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}

	terminals := NewTerminalService(db, cfg)
	session, err := terminals.Open(server.ID, "admin@example.com", "physical ssh validation")
	if err != nil {
		t.Fatalf("open ssh terminal session: %v", err)
	}
	if session.Mode != "ssh" || session.Status != "active" {
		t.Fatalf("expected active ssh terminal session: %#v", session)
	}
	if !strings.Contains(session.Transcript, "SSH terminal session opened on lab-ssh") || strings.Contains(session.Transcript, "Simulated") {
		t.Fatalf("expected real ssh open transcript, got %q", session.Transcript)
	}

	session, err = terminals.RunCommand(session.ID, "admin@example.com", "printf terminal-ok")
	if err != nil {
		t.Fatalf("run ssh terminal command: %v", err)
	}
	if !strings.Contains(session.Transcript, "terminal-ok") || !strings.Contains(session.Transcript, "exit_code=0") {
		t.Fatalf("expected ssh command output and exit code in transcript, got %q", session.Transcript)
	}
}

func newTerminalTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Server{}, &models.Credential{}, &models.SSHAccess{}, &models.TerminalSession{}); err != nil {
		t.Fatalf("migrate terminal test DB: %v", err)
	}
	return db
}
