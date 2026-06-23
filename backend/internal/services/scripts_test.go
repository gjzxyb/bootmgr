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

func TestScriptServiceRunsScriptsOverRealSSH(t *testing.T) {
	addr, _, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	db := newScriptServiceTestDB(t)
	cfg := config.Config{
		CredentialKey:     "script-ssh-test-key",
		SSHOperationsMode: "ssh",
		SSHHostKeyPolicy:  "insecure_ignore",
		SSHConnectTimeout: 5 * time.Second,
	}
	serverID := createScriptSSHServer(t, db, cfg, addr)

	scripts := NewScriptService(db, cfg)
	job, err := scripts.CreateJob("real ssh script", "printf script-ok", "admin@example.com", []uint{serverID}, 1, 5)
	if err != nil {
		t.Fatalf("create ssh script job: %v", err)
	}
	job = waitForScriptJob(t, db, job.ID)
	if job.Status != "success" || strings.TrimSpace(job.ErrorMessage) != "" {
		t.Fatalf("expected successful ssh script job: %#v", job)
	}

	var execution models.ScriptExecution
	if err := db.Where("script_job_id = ?", job.ID).First(&execution).Error; err != nil {
		t.Fatalf("load script execution: %v", err)
	}
	if execution.Status != "success" || execution.ExitCode != 0 || !strings.Contains(execution.Stdout, "script-ok") || strings.Contains(execution.Stdout, "Simulated") {
		t.Fatalf("expected real ssh script stdout and exit code: %#v", execution)
	}
}

func TestScriptServiceRecordsSSHScriptFailure(t *testing.T) {
	addr, _, closeServer := startTestSSHServer(t, "tester", "secret-password")
	defer closeServer()

	db := newScriptServiceTestDB(t)
	cfg := config.Config{
		CredentialKey:     "script-ssh-failure-test-key",
		SSHOperationsMode: "ssh",
		SSHHostKeyPolicy:  "insecure_ignore",
		SSHConnectTimeout: 5 * time.Second,
	}
	serverID := createScriptSSHServer(t, db, cfg, addr)

	scripts := NewScriptService(db, cfg)
	job, err := scripts.CreateJob("real ssh script failure", "printf script-error >&2\nexit 7", "admin@example.com", []uint{serverID}, 1, 5)
	if err != nil {
		t.Fatalf("create ssh script job: %v", err)
	}
	job = waitForScriptJob(t, db, job.ID)
	if job.Status != "failed" || job.ErrorMessage != "one or more script executions failed" {
		t.Fatalf("expected failed ssh script job with generic failure message: %#v", job)
	}

	var execution models.ScriptExecution
	if err := db.Where("script_job_id = ?", job.ID).First(&execution).Error; err != nil {
		t.Fatalf("load script execution: %v", err)
	}
	if execution.Status != "failed" || execution.ExitCode != 7 || !strings.Contains(execution.Stderr, "script-error") {
		t.Fatalf("expected real ssh script stderr and exit code: %#v", execution)
	}
}

func createScriptSSHServer(t *testing.T, db *gorm.DB, cfg config.Config, addr string) uint {
	t.Helper()
	credentials := NewCredentialService(db, cfg)
	cred, err := credentials.Store("script-ssh-password", "ssh", "tester", "secret-password", "tester@example.com")
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
	server := models.Server{AssetNo: "BM-SCRIPT-SSH", Hostname: "script-ssh", Status: "running", Architecture: "x86_64"}
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
	return server.ID
}

func waitForScriptJob(t *testing.T, db *gorm.DB, id uint) models.ScriptJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var job models.ScriptJob
	for time.Now().Before(deadline) {
		if err := db.First(&job, id).Error; err != nil {
			t.Fatalf("load script job: %v", err)
		}
		if job.Status != "running" {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("script job %d did not finish", id)
	return job
}

func newScriptServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Server{}, &models.Credential{}, &models.SSHAccess{}, &models.ScriptJob{}, &models.ScriptExecution{}); err != nil {
		t.Fatalf("migrate script test DB: %v", err)
	}
	return db
}
