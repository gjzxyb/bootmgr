package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/database"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestMVPFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file::memory:?cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: true,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)

	token := loginForTest(t, r)
	var bootstrapAdmin models.User
	if err := db.Where("email = ?", "admin@example.com").First(&bootstrapAdmin).Error; err != nil {
		t.Fatalf("load bootstrap admin: %v", err)
	}
	health, healthHeaders := requestJSONWithHeaders(t, r, http.MethodGet, "/healthz", "", nil, map[string]string{"X-Request-ID": "test-request-001"})
	if healthHeaders.Get("X-Request-ID") != "test-request-001" {
		t.Fatalf("expected X-Request-ID to be propagated, got %q", healthHeaders.Get("X-Request-ID"))
	}
	if health["status"] != "ok" || health["database"] != "ok" || health["redis"] != "disabled" {
		t.Fatalf("expected healthy local service: %#v", health)
	}
	if err := db.Create(&models.NetworkConfig{Name: "PXE test network", Purpose: "deployment", CIDR: "192.0.2.0/24", Status: "enabled", DHCPMode: "proxy", ProxyDHCP: true}).Error; err != nil {
		t.Fatalf("create PXE test deployment network: %v", err)
	}
	readiness, readinessHeaders := requestJSONWithHeaders(t, r, http.MethodGet, "/readyz", "", nil, map[string]string{"X-Request-ID": "test-ready-001"})
	if readinessHeaders.Get("X-Request-ID") != "test-ready-001" {
		t.Fatalf("expected readyz X-Request-ID to be propagated, got %q", readinessHeaders.Get("X-Request-ID"))
	}
	checks, ok := readiness["checks"].([]any)
	if !ok {
		t.Fatalf("expected readyz checks array: %#v", readiness)
	}
	for _, name := range []string{"database", "redis", "image_storage", "bmc_tooling", "config"} {
		if !jsonArrayContainsField(checks, "name", name) {
			t.Fatalf("expected readyz check %s in %#v", name, checks)
		}
	}
	if _, ok := readiness["config_issues"].([]any); !ok {
		t.Fatalf("expected readyz config_issues array: %#v", readiness)
	}
	discoveryScript := requestText(t, r, http.MethodGet, "/boot/ipxe?mac=52-54-00-DD-00-01&arch=arm64&firmware=uefi", "", nil)
	if !contains(discoveryScript, "Baremetal discovery") {
		t.Fatalf("expected discovery script for unknown MAC: %s", discoveryScript)
	}
	var httpBootEvent models.BootEvent
	if err := db.Where("mac = ?", "52:54:00:dd:00:01").Order("id desc").First(&httpBootEvent).Error; err != nil {
		t.Fatalf("expected HTTP iPXE boot event: %v", err)
	}
	if httpBootEvent.Source != "http_ipxe" {
		t.Fatalf("expected HTTP iPXE boot event source, got %#v", httpBootEvent)
	}
	discoveredServers := requestJSON(t, r, http.MethodGet, "/api/v1/servers?keyword=discovered-52-54-00-dd-00-01&page=1&page_size=1", token, nil)
	if intFromJSON(t, discoveredServers, "total") != 1 {
		t.Fatalf("expected PXE discovery to create discovered asset: %#v", discoveredServers)
	}
	discoveredID := int(discoveredServers["items"].([]any)[0].(map[string]any)["id"].(float64))
	discoveredHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(discoveredID)+"/status-history", token, nil)
	if !jsonArrayContainsField(discoveredHistory["_array"].([]any), "reason", "boot.discovery") {
		t.Fatalf("expected PXE discovery status history: %#v", discoveredHistory)
	}
	bootEvent := requestJSON(t, r, http.MethodPost, "/boot/events", "", map[string]any{"mac": "52:54:00:dd:00:01", "architecture": "arm64", "firmware": "uefi"})
	if intFromJSON(t, bootEvent, "server_id") != discoveredID {
		t.Fatalf("expected boot event to match discovered asset: %#v", bootEvent)
	}
	if bootEvent["source"] != "api_event" {
		t.Fatalf("expected manual boot event source api_event: %#v", bootEvent)
	}
	labValidation := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	if labValidation["status"] == "" {
		t.Fatalf("expected lab validation status: %#v", labValidation)
	}
	labChecks, ok := labValidation["checks"].([]any)
	if !ok || !jsonArrayContainsField(labChecks, "name", "pxe_boot_events") || !jsonArrayContainsField(labChecks, "name", "bmc_adapter") || !jsonArrayContainsField(labChecks, "name", "bmc_tooling") || !jsonArrayContainsField(labChecks, "name", "ssh_connectivity") {
		t.Fatalf("expected lab validation checks for PXE/BMC/SSH: %#v", labValidation)
	}
	pxeSummary, ok := labValidation["pxe"].(map[string]any)
	if !ok || intFromJSON(t, pxeSummary, "boot_events") < 1 {
		t.Fatalf("expected lab validation to include PXE boot event evidence: %#v", labValidation)
	}
	if targets, ok := labValidation["targets"].([]any); !ok || !jsonArrayContainsField(targets, "server_id", float64(discoveredID)) {
		t.Fatalf("expected lab validation target matrix to include discovered PXE server: %#v", labValidation)
	}
	inventoryOnly := models.Server{AssetNo: "BM-PXE-TARGET-001", Hostname: "pxe-target-only", PrimaryMAC: "52:54:00:dd:00:99", Status: "ready"}
	if err := db.Create(&inventoryOnly).Error; err != nil {
		t.Fatalf("create inventory-only PXE target: %v", err)
	}
	labValidation = requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	if targets, ok := labValidation["targets"].([]any); !ok || !jsonArrayContainsField(targets, "server_id", float64(inventoryOnly.ID)) {
		t.Fatalf("expected lab validation target matrix to include inventory MAC target: %#v", labValidation)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/run", token, map[string]any{"check_pxe": false, "check_bmc": false, "check_ssh": false}, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/run", token, map[string]any{"check_pxe": true, "check_bmc": false, "check_ssh": false, "pxe_macs": []string{"not-a-mac"}}, http.StatusBadRequest, "system.lab-validation.run")
	strictLabRun := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/run", token, map[string]any{"strict": true, "check_pxe": true, "check_bmc": true, "check_ssh": true}, "system.lab-validation.run")
	if strictLabRun["status"] != "error" || !jsonArrayContainsField(strictLabRun["run_results"].([]any), "kind", "strict_physical_targets") {
		t.Fatalf("expected strict lab validation to require physical targets: %#v", strictLabRun)
	}
	strictLabRunID := intFromJSON(t, strictLabRun, "run_id")
	var strictRunResults int64
	if err := db.Model(&models.LabValidationRunResult{}).Where("run_id = ? AND kind = ?", strictLabRunID, "strict_physical_targets").Count(&strictRunResults).Error; err != nil {
		t.Fatalf("count strict lab validation results: %v", err)
	}
	if strictRunResults == 0 {
		t.Fatalf("expected strict lab validation failures to be persisted")
	}
	labRun := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/run", token, map[string]any{"check_pxe": false, "check_bmc": false, "check_ssh": false}, "system.lab-validation.run")
	if labRun["checks"] == nil || intFromJSON(t, labRun, "run_id") == 0 {
		t.Fatalf("expected lab validation run to return a report: %#v", labRun)
	}
	if recentRuns, ok := labRun["recent_runs"].([]any); !ok || !jsonArrayContainsField(recentRuns, "id", float64(intFromJSON(t, labRun, "run_id"))) {
		t.Fatalf("expected lab validation report to include recent run history: %#v", labRun)
	}
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/system/lab-validation/runs/not-a-number", token, nil, http.StatusBadRequest, "")
	labRunDetail := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation/runs/"+itoa(intFromJSON(t, strictLabRun, "run_id")), token, nil)
	if intFromJSON(t, labRunDetail["run"].(map[string]any), "id") != intFromJSON(t, strictLabRun, "run_id") || !jsonArrayContainsField(labRunDetail["results"].([]any), "kind", "strict_physical_targets") {
		t.Fatalf("expected lab validation run detail with persisted results: %#v", labRunDetail)
	}
	labRunBundle := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation/runs/"+itoa(intFromJSON(t, strictLabRun, "run_id"))+"/evidence-bundle", token, nil)
	if intFromJSON(t, labRunBundle["run"].(map[string]any), "id") != intFromJSON(t, strictLabRun, "run_id") || !jsonArrayContainsField(labRunBundle["results"].([]any), "kind", "strict_physical_targets") || labRunBundle["environment"] == nil {
		t.Fatalf("expected lab validation evidence bundle with run, results, and environment: %#v", labRunBundle)
	}
	if targets, ok := labRunBundle["targets"].([]any); !ok || !jsonArrayContainsField(targets, "server_id", float64(discoveredID)) {
		t.Fatalf("expected lab validation evidence bundle to include target matrix: %#v", labRunBundle)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, map[string]any{"kind": "pxe", "status": "ok", "summary": "physical PXE client reached iPXE"}, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, map[string]any{"kind": "pxe", "status": "ok", "summary": "physical PXE client reached iPXE"}, http.StatusBadRequest, "system.lab-validation.evidence")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, map[string]any{"kind": "pxe", "subject": "52:54:00:dd:00:01", "status": "ok", "summary": "physical PXE client reached iPXE"}, http.StatusBadRequest, "system.lab-validation.evidence")
	evidence := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, map[string]any{"kind": "pxe", "subject": "52:54:00:dd:00:01", "status": "ok", "summary": "physical PXE client reached iPXE", "details": "boot.ipxe loaded from isolated deployment VLAN", "run_id": intFromJSON(t, labRun, "run_id"), "boot_event_id": httpBootEvent.ID}, "system.lab-validation.evidence")
	if evidence["kind"] != "pxe" || evidence["status"] != "ok" || evidence["created_by"] != "admin@example.com" {
		t.Fatalf("expected recorded lab evidence: %#v", evidence)
	}
	if intFromJSON(t, evidence, "boot_event_id") != int(httpBootEvent.ID) {
		t.Fatalf("expected lab evidence to reference boot event: %#v", evidence)
	}
	if intFromJSON(t, evidence, "run_id") != intFromJSON(t, labRun, "run_id") {
		t.Fatalf("expected lab evidence to reference validation run: %#v", evidence)
	}
	labWithEvidence := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	recentEvidence, ok := labWithEvidence["recent_evidence"].([]any)
	if !ok || !jsonArrayContainsField(recentEvidence, "summary", "physical PXE client reached iPXE") {
		t.Fatalf("expected lab validation evidence in report: %#v", labWithEvidence)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "not-an-email", "name": "Bad User", "role": "operator", "password": "Operator@123456"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "weak@example.com", "name": "Weak User", "role": "operator", "password": "short"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "badrole@example.com", "name": "Bad Role", "role": "owner", "password": "Operator@123456"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/users/"+itoa(int(bootstrapAdmin.ID)), token, map[string]any{"role": "viewer"}, http.StatusConflict, "")
	spareAdmin := requestJSON(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "spare-admin@example.com", "name": "Spare Admin", "role": "admin", "password": "Admin@123456"})
	spareAdminID := intFromJSON(t, spareAdmin, "id")
	demotedSpareAdmin := requestJSON(t, r, http.MethodPatch, "/api/v1/users/"+itoa(spareAdminID), token, map[string]any{"role": "viewer", "name": "Spare Viewer"})
	if demotedSpareAdmin["role"] != "viewer" || demotedSpareAdmin["name"] != "Spare Viewer" {
		t.Fatalf("expected spare admin to be demoted while bootstrap admin remains: %#v", demotedSpareAdmin)
	}
	userResp := requestJSON(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "operator@example.com", "name": "Ops Operator", "role": "operator", "password": "Operator@123456"})
	userID := intFromJSON(t, userResp, "id")
	filteredUsers := requestJSON(t, r, http.MethodGet, "/api/v1/users?keyword=operator&role=operator&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredUsers, "total") != 1 || intFromJSON(t, filteredUsers, "page_size") != 1 {
		t.Fatalf("expected filtered user pagination: %#v", filteredUsers)
	}
	userItems := filteredUsers["items"].([]any)
	if len(userItems) != 1 || userItems[0].(map[string]any)["email"] != "operator@example.com" {
		t.Fatalf("expected filtered user row: %#v", filteredUsers)
	}
	updatedUser := requestJSON(t, r, http.MethodPatch, "/api/v1/users/"+itoa(userID), token, map[string]any{"role": "viewer", "name": "Readonly Operator"})
	if updatedUser["role"] != "viewer" || updatedUser["name"] != "Readonly Operator" {
		t.Fatalf("expected updated user: %#v", updatedUser)
	}
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/users/"+itoa(userID), token, map[string]any{"role": "owner"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/users/"+itoa(userID)+"/reset-password", token, map[string]any{"password": "weakpass"}, http.StatusBadRequest, "")
	requestJSON(t, r, http.MethodPost, "/api/v1/users/"+itoa(userID)+"/reset-password", token, map[string]any{"password": "Viewer@123456"})
	operatorResp := requestJSON(t, r, http.MethodPost, "/api/v1/users", token, map[string]any{"email": "operator2@example.com", "name": "Ops Operator 2", "role": "operator", "password": "Operator@123456"})
	operatorToken := loginForTestWith(t, r, " "+strings.ToUpper(operatorResp["email"].(string))+" ", "Operator@123456")
	viewerToken := loginForTestWith(t, r, "operator@example.com", "Viewer@123456")
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/servers", "", nil, http.StatusUnauthorized, "")
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/users", operatorToken, nil, http.StatusForbidden, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", viewerToken, map[string]any{"asset_no": "VIEWER-DENIED"}, http.StatusForbidden, "")
	operatorCreatedServer := requestJSON(t, r, http.MethodPost, "/api/v1/servers", operatorToken, map[string]any{"asset_no": "BM-OP-001", "hostname": "operator-created", "primary_mac": "52:54:00:cc:00:01", "architecture": "x86_64"})
	if operatorCreatedServer["asset_no"] != "BM-OP-001" {
		t.Fatalf("expected operator to create server: %#v", operatorCreatedServer)
	}
	operatorCreatedServerID := intFromJSON(t, operatorCreatedServer, "id")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/servers/"+itoa(operatorCreatedServerID), operatorToken, nil, http.StatusForbidden, "server.delete")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/servers/"+itoa(operatorCreatedServerID), token, nil, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "bad tenant", "name": "Bad Tenant"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "bad-status", "name": "Bad Status", "status": "archived"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "bad-quota", "name": "Bad Quota", "quota": []string{"servers"}}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "negative-quota", "name": "Negative Quota", "quota": map[string]any{"servers": -1}}, http.StatusBadRequest, "")
	tenantResp := requestJSON(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": " tenant-a ", "name": " Tenant A ", "owner": " team-a ", "quota": map[string]any{"servers": 10}})
	if tenantResp["tenant_id"] != "tenant-a" || tenantResp["name"] != "Tenant A" || tenantResp["owner"] != "team-a" {
		t.Fatalf("expected tenant fields to be trimmed: %#v", tenantResp)
	}
	tenantID := intFromJSON(t, tenantResp, "id")
	filteredTenants := requestJSON(t, r, http.MethodGet, "/api/v1/tenants?keyword=tenant-a&status=active&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredTenants, "total") != 1 || intFromJSON(t, filteredTenants, "page_size") != 1 {
		t.Fatalf("expected filtered tenant pagination: %#v", filteredTenants)
	}
	tenantItems := filteredTenants["items"].([]any)
	if len(tenantItems) != 1 || tenantItems[0].(map[string]any)["tenant_id"] != "tenant-a" {
		t.Fatalf("expected filtered tenant row: %#v", filteredTenants)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "quota-decimal", "name": "Quota Decimal", "quota": map[string]any{"servers": 1.5}}, http.StatusBadRequest, "")
	quotaTenant := requestJSON(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "tenant-quota", "name": "Tenant Quota", "quota": map[string]any{"servers": 1}})
	quotaTenantID := intFromJSON(t, quotaTenant, "id")
	requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-QUOTA-001", "hostname": "quota-node-1", "tenant_id": "tenant-quota"})
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-QUOTA-002", "hostname": "quota-node-2", "tenant_id": "tenant-quota"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/tenants/"+itoa(quotaTenantID), token, map[string]any{"quota": map[string]any{"servers": 0}}, http.StatusBadRequest, "")
	moveCandidate := requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-QUOTA-MOVE", "hostname": "quota-move"})
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(intFromJSON(t, moveCandidate, "id")), token, map[string]any{"tenant_id": "tenant-quota"}, http.StatusBadRequest, "")
	requestJSON(t, r, http.MethodPost, "/api/v1/tenants", token, map[string]any{"tenant_id": "tenant-import-quota", "name": "Tenant Import Quota", "quota": map[string]any{"servers": 1}})
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{{"asset_no": "BM-QUOTA-I1", "hostname": "quota-import-1", "tenant_id": "tenant-import-quota"}, {"asset_no": "BM-QUOTA-I2", "hostname": "quota-import-2", "tenant_id": "tenant-import-quota"}}}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/tenants/"+itoa(tenantID), token, map[string]any{"status": "archived"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/tenants/"+itoa(tenantID), token, map[string]any{"quota": []string{"servers"}}, http.StatusBadRequest, "")
	updatedTenant := requestJSON(t, r, http.MethodPatch, "/api/v1/tenants/"+itoa(tenantID), token, map[string]any{"tenant_id": "evil", "status": "disabled", "owner": " platform ", "quota": map[string]any{"servers": 5}})
	if updatedTenant["tenant_id"] != "tenant-a" || updatedTenant["status"] != "disabled" || updatedTenant["owner"] != "platform" {
		t.Fatalf("expected updated tenant: %#v", updatedTenant)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-BAD-TENANT", "hostname": "bad-tenant", "tenant_id": "missing-tenant"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-DISABLED-TENANT", "hostname": "disabled-tenant", "tenant_id": "tenant-a"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"owner": "nobody"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-BAD-STATUS", "status": "unknown"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-BAD-ARCH", "architecture": "sparc"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-BAD-IP", "primary_ip": "999.1.1.1"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-BAD-MAC", "primary_mac": "not-a-mac"}, http.StatusBadRequest, "")

	serverResp := requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-T-001", "hostname": "Test-Node", "primary_ip": "192.168.200.50", "primary_mac": "52-54-00-AA-BB-CC", "architecture": "x86_64", "tenant_id": "default"})
	serverID := intFromJSON(t, serverResp, "id")
	if serverResp["tenant_id"] != "default" || serverResp["hostname"] != "test-node" || serverResp["primary_ip"] != "192.168.200.50" || serverResp["primary_mac"] != "52:54:00:aa:bb:cc" {
		t.Fatalf("expected server to keep active tenant assignment and normalize identity fields: %#v", serverResp)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-T-001", "hostname": "duplicate-asset"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-DUP-MAC", "primary_mac": "52-54-00-aa-bb-cc"}, http.StatusBadRequest, "")
	tenantFilteredServers := requestJSON(t, r, http.MethodGet, "/api/v1/servers?tenant_id=default&keyword=test-node&page=1&page_size=1", token, nil)
	if intFromJSON(t, tenantFilteredServers, "total") != 1 {
		t.Fatalf("expected tenant-filtered server query: %#v", tenantFilteredServers)
	}
	terminalCandidate := requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-TERM-001", "hostname": "terminal-candidate", "status": "ready", "tenant_id": "default"})
	terminalCandidateID := intFromJSON(t, terminalCandidate, "id")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(terminalCandidateID), token, map[string]any{"status": "scrapped"}, http.StatusPreconditionRequired, "")
	terminalCandidate = requestJSONWithConfirm(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(terminalCandidateID), token, map[string]any{"status": "scrapped"}, "server.status-terminal")
	if terminalCandidate["status"] != "scrapped" {
		t.Fatalf("expected confirmed terminal status update: %#v", terminalCandidate)
	}
	history := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	if len(history["_array"].([]any)) != 1 {
		t.Fatalf("expected initial status history entry: %#v", history)
	}
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"status": "unknown"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"architecture": "sparc"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"primary_ip": "999.1.1.1"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"hostname": "operator-created"}, http.StatusBadRequest, "")
	deleteCandidate := requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-DELETE-001", "hostname": "delete-candidate", "tenant_id": "default"})
	deleteCandidateID := intFromJSON(t, deleteCandidate, "id")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/servers/"+itoa(deleteCandidateID), token, nil, http.StatusNoContent, "server.delete")
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/servers/"+itoa(deleteCandidateID), token, nil, http.StatusNotFound, "")
	deleteCandidateHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(deleteCandidateID)+"/status-history", token, nil)
	if len(deleteCandidateHistory["_array"].([]any)) != 0 {
		t.Fatalf("deleted server status history should be removed: %#v", deleteCandidateHistory)
	}
	var serverDeleteAudits int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "server.delete", "server", itoa(deleteCandidateID)).Count(&serverDeleteAudits).Error; err != nil {
		t.Fatalf("count server delete audit logs: %v", err)
	}
	if serverDeleteAudits != 1 {
		t.Fatalf("expected one server.delete audit, got %d", serverDeleteAudits)
	}
	taggedServer := requestJSON(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"owner": "platform", "tags": []string{"mvp-tag", "gpu"}, "retired_at": "2099-01-01T00:00:00Z"})
	if !jsonArrayContainsString(taggedServer["tags"].([]any), "mvp-tag") {
		t.Fatalf("expected server tags to persist: %#v", taggedServer)
	}
	taggedServers := requestJSON(t, r, http.MethodGet, "/api/v1/servers?keyword=mvp-tag&page=1&page_size=10", token, nil)
	if intFromJSON(t, taggedServers, "total") < 1 {
		t.Fatalf("expected keyword search to include tags: %#v", taggedServers)
	}
	history = requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	if len(history["_array"].([]any)) != 1 {
		t.Fatalf("non-status update should not create status history entry: %#v", history)
	}
	requestJSON(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"status": "ready"})
	history = requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	if len(history["_array"].([]any)) != 2 {
		t.Fatalf("expected status update history entry: %#v", history)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{{"asset_no": "BM-IMPORT-BAD-STATUS", "status": "unknown"}}}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{{"asset_no": "BM-IMPORT-BAD-IP", "primary_ip": "999.1.1.1"}}}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{{"asset_no": "BM-IMPORT-DUP-MAC", "primary_mac": "52:54:00:aa:bb:cc"}}}, http.StatusBadRequest, "")
	importResp := requestJSON(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{
		{"asset_no": "BM-IMPORT-001", "hostname": "import-node-01", "primary_mac": "52:54:00:bb:00:01", "architecture": "x86_64", "status": "ready", "owner": "ops"},
		{"asset_no": "BM-IMPORT-002", "hostname": "import-node-02", "primary_mac": "52:54:00:bb:00:02", "architecture": "arm64", "status": "discovered", "owner": "qa"},
	}})
	if intFromJSON(t, importResp, "created") != 2 {
		t.Fatalf("expected two imported servers: %#v", importResp)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/import", token, map[string]any{"servers": []map[string]any{{"asset_no": "BM-IMPORT-BAD-TENANT", "hostname": "bad-import", "tenant_id": "tenant-a"}}}, http.StatusBadRequest, "")
	importedRows := importResp["servers"].([]any)
	importedID := int(importedRows[0].(map[string]any)["id"].(float64))
	importedIPMIID := int(importedRows[1].(map[string]any)["id"].(float64))
	importHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(importedID)+"/status-history", token, nil)
	if len(importHistory["_array"].([]any)) != 1 {
		t.Fatalf("expected imported server status history: %#v", importHistory)
	}
	filteredServers := requestJSON(t, r, http.MethodGet, "/api/v1/servers?status=ready&owner=ops&keyword=import-node&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredServers, "total") != 1 || intFromJSON(t, filteredServers, "page_size") != 1 {
		t.Fatalf("expected filtered server pagination: %#v", filteredServers)
	}
	filteredServerItems := filteredServers["items"].([]any)
	if len(filteredServerItems) != 1 || filteredServerItems[0].(map[string]any)["hostname"] != "import-node-01" {
		t.Fatalf("expected filtered server row: %#v", filteredServers)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Missing target", "script": "uptime", "server_ids": []int{999999}}, http.StatusBadRequest, "ops.script.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Duplicate target", "script": "uptime", "server_ids": []int{serverID, serverID}}, http.StatusBadRequest, "ops.script.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Bad concurrency", "script": "uptime", "server_ids": []int{serverID}, "concurrency": 51}, http.StatusBadRequest, "ops.script.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Bad timeout", "script": "uptime", "server_ids": []int{serverID}, "timeout_seconds": 3601}, http.StatusBadRequest, "ops.script.create")
	scriptJob := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Inspect uptime", "script": "uptime", "server_ids": []int{serverID, importedID}, "timeout_seconds": 30}, "ops.script.create")
	scriptJobID := intFromJSON(t, scriptJob, "id")
	if int(scriptJob["concurrency"].(float64)) != 2 {
		t.Fatalf("script job concurrency should be capped to target count: %#v", scriptJob)
	}
	time.Sleep(600 * time.Millisecond)
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/ops/script-jobs/999999/results", token, nil, http.StatusNotFound, "")
	scriptResults := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs/"+itoa(scriptJobID)+"/results", token, nil)
	if len(scriptResults["_array"].([]any)) != 2 {
		t.Fatalf("expected script results for two servers: %#v", scriptResults)
	}
	scriptJobs := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs", token, nil)
	if len(scriptJobs["_array"].([]any)) == 0 {
		t.Fatalf("expected script job list")
	}
	filteredScriptJobs := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs?keyword=uptime&status=success&requested_by=admin@example.com&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredScriptJobs, "total") != 1 || intFromJSON(t, filteredScriptJobs, "page_size") != 1 {
		t.Fatalf("expected filtered script job pagination: %#v", filteredScriptJobs)
	}
	filteredScriptItems := filteredScriptJobs["items"].([]any)
	if len(filteredScriptItems) != 1 || filteredScriptItems[0].(map[string]any)["name"] != "Inspect uptime" {
		t.Fatalf("expected filtered script job row: %#v", filteredScriptJobs)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/log-collections", token, map[string]any{"server_ids": []int{serverID}}, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/log-collections", token, map[string]any{"server_ids": []int{serverID, serverID}}, http.StatusBadRequest, "ops.logs.collect")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/log-collections", token, map[string]any{"server_ids": []int{serverID}, "sources": []string{"syslog", "bad-source"}}, http.StatusBadRequest, "ops.logs.collect")
	logCollection := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/log-collections", token, map[string]any{"server_ids": []int{serverID, importedID}}, "ops.logs.collect")
	if intFromJSON(t, logCollection, "requested") != 2 || intFromJSON(t, logCollection, "succeeded") != 2 || intFromJSON(t, logCollection, "events_created") != 6 {
		t.Fatalf("expected default log collection to create three events per server: %#v", logCollection)
	}
	dmesgLogs := requestJSON(t, r, http.MethodGet, "/api/v1/log-events?server_id="+itoa(serverID)+"&source=dmesg&page=1&page_size=10", token, nil)
	if intFromJSON(t, dmesgLogs, "total") == 0 || !jsonArrayFieldContains(dmesgLogs["items"].([]any), "message", "Simulated dmesg collection") {
		t.Fatalf("expected dmesg log event from collection: %#v", dmesgLogs)
	}
	mixedLogCollection := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/log-collections", token, map[string]any{"server_ids": []int{serverID, terminalCandidateID, 999999}, "sources": []string{"hardware"}}, "ops.logs.collect")
	if intFromJSON(t, mixedLogCollection, "requested") != 3 || intFromJSON(t, mixedLogCollection, "succeeded") != 1 || intFromJSON(t, mixedLogCollection, "failed") != 2 {
		t.Fatalf("expected mixed log collection results: %#v", mixedLogCollection)
	}
	mixedLogResults := mixedLogCollection["results"].([]any)
	if !jsonArrayFieldContains(mixedLogResults, "error", "not eligible") || !jsonArrayFieldContains(mixedLogResults, "error", "server not found") {
		t.Fatalf("expected log collection per-target failures: %#v", mixedLogCollection)
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/inventory", token, map[string]any{"raw_payload": []string{"bad"}}, http.StatusBadRequest, "")
	inventory := requestJSON(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/inventory", token, map[string]any{
		"cpu_summary": "2 x AMD EPYC 9354", "memory_summary": "512GB DDR5", "disk_summary": "8 x 3.84TB NVMe",
		"network_summary": "4 x 25GbE", "gpu_summary": "none", "raid_summary": "HBA passthrough", "raw_payload": map[string]any{"source": "test"},
	})
	if intFromJSON(t, inventory, "server_id") != serverID {
		t.Fatalf("inventory not bound to server")
	}
	bom := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bom", token, nil)
	if bom["cpu_summary"] != "2 x AMD EPYC 9354" || bom["asset_no"] != "BM-T-001" {
		t.Fatalf("unexpected BOM response: %#v", bom)
	}
	serverCSV := requestText(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bom.csv", token, nil)
	if !contains(serverCSV, "asset_no,hostname") || !contains(serverCSV, "2 x AMD EPYC 9354") {
		t.Fatalf("unexpected server BOM CSV: %s", serverCSV)
	}
	allCSV := requestText(t, r, http.MethodGet, "/api/v1/bom.csv", token, nil)
	if !contains(allCSV, "BM-T-001") || !contains(allCSV, "512GB DDR5") || !contains(allCSV, "BM-IMPORT-001") {
		t.Fatalf("unexpected all BOM CSV: %s", allCSV)
	}

	imagePath := tempImageFile(t, cfg.ImageStorageDir)
	outsideImagePath := tempImageFile(t, t.TempDir())
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "No path", "architecture": "x86_64"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "Outside path", "file_path": outsideImagePath}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "Bad image status", "file_path": imagePath, "status": "archived"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "Bad image arch", "file_path": imagePath, "architecture": "sparc"}, http.StatusBadRequest, "")
	imageResp := requestJSON(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "Test Ubuntu", "os_family": "ubuntu", "os_version": "24.04", "architecture": "x86_64", "file_path": imagePath, "created_by": "attacker@example.com", "test_status": "tested_passed", "sha256": "client-supplied", "size_bytes": 999})
	imageID := intFromJSON(t, imageResp, "id")
	if imageResp["created_by"] != "admin@example.com" {
		t.Fatalf("image created_by should come from actor: %#v", imageResp)
	}
	if imageResp["test_status"] != "untested" || imageResp["sha256"] != "" || int(imageResp["size_bytes"].(float64)) != 0 {
		t.Fatalf("manual image registration should not accept client verification fields: %#v", imageResp)
	}
	requestExpectStatus(t, r, http.MethodGet, "/images/"+itoa(imageID)+"/file", "", nil, http.StatusForbidden, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/images/"+itoa(imageID), token, map[string]any{"status": "archived"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/images/"+itoa(imageID), token, map[string]any{"architecture": "sparc"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/images/"+itoa(imageID), token, map[string]any{"file_path": ""}, http.StatusBadRequest, "")
	patchedImage := requestJSON(t, r, http.MethodPatch, "/api/v1/images/"+itoa(imageID), token, map[string]any{"test_status": "tested_passed", "sha256": "client-supplied", "size_bytes": 999})
	if patchedImage["test_status"] != "untested" || patchedImage["sha256"] == "client-supplied" || int(patchedImage["size_bytes"].(float64)) != 0 {
		t.Fatalf("image update should ignore client verification fields: %#v", patchedImage)
	}
	filteredImages := requestJSON(t, r, http.MethodGet, "/api/v1/images?keyword=Ubuntu&os_family=ubuntu&architecture=x86_64&status=enabled&test_status=untested&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredImages, "total") != 1 || intFromJSON(t, filteredImages, "page_size") != 1 {
		t.Fatalf("expected filtered image pagination: %#v", filteredImages)
	}
	filteredImageItems := filteredImages["items"].([]any)
	if len(filteredImageItems) != 1 || filteredImageItems[0].(map[string]any)["name"] != "Test Ubuntu" {
		t.Fatalf("expected filtered image row: %#v", filteredImages)
	}
	uploadedContent := []byte("uploaded test image content")
	uploadedImage := requestMultipartImage(t, r, token, map[string]string{"name": "Uploaded Ubuntu", "os_family": "ubuntu", "os_version": "24.04", "architecture": "x86_64"}, "ubuntu-24.04-upload.iso", uploadedContent)
	uploadedImageID := intFromJSON(t, uploadedImage, "id")
	if uploadedImage["test_status"] != "tested_passed" || uploadedImage["status"] != "enabled" {
		t.Fatalf("expected uploaded image to be enabled and verified: %#v", uploadedImage)
	}
	if intFromJSON(t, uploadedImage, "size_bytes") != len(uploadedContent) {
		t.Fatalf("unexpected uploaded image size: %#v", uploadedImage)
	}
	uploadedHash := sha256.Sum256(uploadedContent)
	if uploadedImage["sha256"] != hex.EncodeToString(uploadedHash[:]) {
		t.Fatalf("unexpected uploaded image hash: %#v", uploadedImage)
	}
	downloadedImage := requestText(t, r, http.MethodGet, "/images/"+itoa(uploadedImageID)+"/file", "", nil)
	if downloadedImage != string(uploadedContent) {
		t.Fatalf("downloaded image content did not match upload: %q", downloadedImage)
	}
	tempDeleteImage := requestJSON(t, r, http.MethodPost, "/api/v1/images", token, map[string]any{"name": "Temporary delete image", "os_family": "ubuntu", "os_version": "24.04", "architecture": "x86_64", "file_path": imagePath})
	beforeDeleteDashboard := requestJSON(t, r, http.MethodGet, "/api/v1/dashboard", token, nil)
	requestJSONWithConfirm(t, r, http.MethodDelete, "/api/v1/images/"+itoa(intFromJSON(t, tempDeleteImage, "id")), token, nil, "image.delete")
	afterDeleteDashboard := requestJSON(t, r, http.MethodGet, "/api/v1/dashboard", token, nil)
	if intFromJSON(t, beforeDeleteDashboard, "images") != intFromJSON(t, afterDeleteDashboard, "images")+1 {
		t.Fatalf("dashboard image count should ignore soft-deleted images: before=%#v after=%#v", beforeDeleteDashboard, afterDeleteDashboard)
	}
	installTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/install-templates", token, nil)
	workflowTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/workflow-templates", token, nil)
	installTemplateRows := installTemplates["_array"].([]any)
	for _, osFamily := range []string{"ubuntu", "rocky", "debian"} {
		if !jsonArrayContainsField(installTemplateRows, "os_family", osFamily) {
			t.Fatalf("expected seeded %s install template: %#v", osFamily, installTemplates)
		}
	}
	installTemplateID := idByField(t, installTemplateRows, "os_family", "ubuntu")
	workflowTemplateID := int(workflowTemplates["_array"].([]any)[0].(map[string]any)["id"].(float64))
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/install-templates", token, map[string]any{"name": "Missing content", "template_type": "cloud-init"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/install-templates", token, map[string]any{"name": "Bad type", "template_type": "raw", "content": "echo bad"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/install-templates", token, map[string]any{"name": "Bad schema", "template_type": "cloud-init", "content": "#cloud-config", "variables_schema": []string{"hostname"}}, http.StatusBadRequest, "")
	customInstall := requestJSON(t, r, http.MethodPost, "/api/v1/install-templates", token, map[string]any{"id": 999, "name": "Test autoinstall", "os_family": "ubuntu", "os_version": "24.04", "template_type": "cloud-init", "content": "#cloud-config\nhostname: {{hostname}}\n", "variables_schema": map[string]any{"hostname": "string"}, "created_by": "attacker@example.com"})
	customInstallID := intFromJSON(t, customInstall, "id")
	if customInstallID == 999 || customInstall["created_by"] != "admin@example.com" {
		t.Fatalf("install template should ignore client system fields: %#v", customInstall)
	}
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/install-templates/"+itoa(customInstallID), token, map[string]any{"status": "archived"}, http.StatusBadRequest, "")
	protectedInstall := requestJSON(t, r, http.MethodPatch, "/api/v1/install-templates/"+itoa(customInstallID), token, map[string]any{"created_by": "attacker@example.com", "updated_at": "2099-01-01T00:00:00Z", "content": "#cloud-config\nhostname: {{hostname}}\nupdated: true\n"})
	if protectedInstall["created_by"] != "admin@example.com" {
		t.Fatalf("install template update should preserve created_by: %#v", protectedInstall)
	}
	disabledInstall := requestJSON(t, r, http.MethodPatch, "/api/v1/install-templates/"+itoa(customInstallID), token, map[string]any{"status": "disabled"})
	if disabledInstall["status"] != "disabled" {
		t.Fatalf("expected install template to be disabled: %#v", disabledInstall)
	}
	filteredInstallTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/install-templates?keyword=autoinstall&os_family=ubuntu&template_type=cloud-init&status=disabled&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredInstallTemplates, "total") != 1 || intFromJSON(t, filteredInstallTemplates, "page_size") != 1 {
		t.Fatalf("expected filtered install template pagination: %#v", filteredInstallTemplates)
	}
	filteredInstallItems := filteredInstallTemplates["items"].([]any)
	if len(filteredInstallItems) != 1 || filteredInstallItems[0].(map[string]any)["name"] != "Test autoinstall" {
		t.Fatalf("expected filtered install template row: %#v", filteredInstallTemplates)
	}
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/install-templates/"+itoa(customInstallID), operatorToken, nil, http.StatusForbidden, "install_template.delete")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/install-templates/"+itoa(customInstallID), token, nil, http.StatusPreconditionRequired, "")
	requestJSONWithConfirm(t, r, http.MethodDelete, "/api/v1/install-templates/"+itoa(customInstallID), token, nil, "install_template.delete")
	deletedInstallTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/install-templates?keyword=Test%20autoinstall&page=1&page_size=1", token, nil)
	if intFromJSON(t, deletedInstallTemplates, "total") != 0 {
		t.Fatalf("expected deleted install template to disappear from listing: %#v", deletedInstallTemplates)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/workflow-templates", token, map[string]any{"name": "Missing definition"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/workflow-templates", token, map[string]any{"name": "Bad workflow", "definition": map[string]any{"steps": []map[string]any{}}}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/workflow-templates", token, map[string]any{"name": "Bad step", "definition": map[string]any{"steps": []map[string]any{{"name": "Render"}}}}, http.StatusBadRequest, "")
	customWorkflow := requestJSON(t, r, http.MethodPost, "/api/v1/workflow-templates", token, map[string]any{"id": 999, "name": "Test workflow", "version": "v1", "definition": map[string]any{"steps": []map[string]any{{"name": " Render ", "action": " render_ipxe "}}}, "created_by": "attacker@example.com"})
	customWorkflowID := intFromJSON(t, customWorkflow, "id")
	if customWorkflowID == 999 || customWorkflow["created_by"] != "admin@example.com" {
		t.Fatalf("workflow template should ignore client system fields: %#v", customWorkflow)
	}
	workflowDefinition := customWorkflow["definition"].(map[string]any)
	workflowSteps := workflowDefinition["steps"].([]any)
	if workflowSteps[0].(map[string]any)["name"] != "Render" || workflowSteps[0].(map[string]any)["action"] != "render_ipxe" {
		t.Fatalf("workflow step fields should be trimmed: %#v", customWorkflow)
	}
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/workflow-templates/"+itoa(customWorkflowID), token, map[string]any{"status": "archived"}, http.StatusBadRequest, "")
	protectedWorkflow := requestJSON(t, r, http.MethodPatch, "/api/v1/workflow-templates/"+itoa(customWorkflowID), token, map[string]any{"created_by": "attacker@example.com", "description": "Updated by test"})
	if protectedWorkflow["created_by"] != "admin@example.com" {
		t.Fatalf("workflow template update should preserve created_by: %#v", protectedWorkflow)
	}
	disabledWorkflow := requestJSON(t, r, http.MethodPatch, "/api/v1/workflow-templates/"+itoa(customWorkflowID), token, map[string]any{"status": "disabled"})
	if disabledWorkflow["status"] != "disabled" {
		t.Fatalf("expected workflow template to be disabled: %#v", disabledWorkflow)
	}
	filteredWorkflowTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/workflow-templates?keyword=workflow&version=v1&status=disabled&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredWorkflowTemplates, "total") != 1 || intFromJSON(t, filteredWorkflowTemplates, "page_size") != 1 {
		t.Fatalf("expected filtered workflow template pagination: %#v", filteredWorkflowTemplates)
	}
	filteredWorkflowItems := filteredWorkflowTemplates["items"].([]any)
	if len(filteredWorkflowItems) != 1 || filteredWorkflowItems[0].(map[string]any)["name"] != "Test workflow" {
		t.Fatalf("expected filtered workflow template row: %#v", filteredWorkflowTemplates)
	}
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/workflow-templates/"+itoa(customWorkflowID), operatorToken, nil, http.StatusForbidden, "workflow_template.delete")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/workflow-templates/"+itoa(customWorkflowID), token, nil, http.StatusPreconditionRequired, "")
	requestJSONWithConfirm(t, r, http.MethodDelete, "/api/v1/workflow-templates/"+itoa(customWorkflowID), token, nil, "workflow_template.delete")
	deletedWorkflowTemplates := requestJSON(t, r, http.MethodGet, "/api/v1/workflow-templates?keyword=Test%20workflow&page=1&page_size=1", token, nil)
	if intFromJSON(t, deletedWorkflowTemplates, "total") != 0 {
		t.Fatalf("expected deleted workflow template to disappear from listing: %#v", deletedWorkflowTemplates)
	}

	networks := requestJSON(t, r, http.MethodGet, "/api/v1/network-configs?purpose=deployment&status=enabled&page=1&page_size=10", token, nil)
	if intFromJSON(t, networks, "total") < 1 {
		t.Fatalf("expected demo deployment network: %#v", networks)
	}
	networkItems := networks["items"].([]any)
	networkID := 0
	for _, item := range networkItems {
		network := item.(map[string]any)
		if network["name"] == "Demo deployment network" {
			networkID = int(network["id"].(float64))
			break
		}
	}
	if networkID == 0 {
		t.Fatalf("expected seeded demo deployment network in listing: %#v", networks)
	}
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(networkID), token, map[string]any{"cidr": "not-a-cidr"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(networkID), token, map[string]any{"gateway": "203.0.113.1"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(networkID), token, map[string]any{"dhcp_mode": "rogue"}, http.StatusBadRequest, "")
	updatedNetwork := requestJSON(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(networkID), token, map[string]any{"description": "updated by test"})
	if updatedNetwork["description"] != "updated by test" {
		t.Fatalf("expected network config update: %#v", updatedNetwork)
	}
	updatedNetwork = requestJSON(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(networkID), token, map[string]any{
		"name":        "Updated deployment network",
		"cidr":        "192.168.210.0/24",
		"gateway":     "192.168.210.1",
		"dns":         "192.168.210.2, 192.168.210.3",
		"vlan_id":     210,
		"dhcp_mode":   "proxy",
		"proxy_dhcp":  false,
		"status":      "disabled",
		"description": "full update by test",
	})
	if updatedNetwork["name"] != "Updated deployment network" || updatedNetwork["cidr"] != "192.168.210.0/24" || updatedNetwork["gateway"] != "192.168.210.1" || updatedNetwork["dns"] != "192.168.210.2,192.168.210.3" || int(updatedNetwork["vlan_id"].(float64)) != 210 || updatedNetwork["proxy_dhcp"] != false || updatedNetwork["status"] != "disabled" {
		t.Fatalf("expected full network config update: %#v", updatedNetwork)
	}
	db.Where("purpose = ?", "deployment").Delete(&models.NetworkConfig{})
	preflightResp := requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": importedID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(preflightResp["problems"].([]any), "deployment network not configured") {
		t.Fatalf("expected deployment preflight to require deployment network: %#v", preflightResp)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Bad CIDR", "purpose": "deployment", "cidr": "192.168.200.300/24"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Bad gateway", "purpose": "deployment", "cidr": "192.168.200.0/24", "gateway": "192.168.201.1"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Bad VLAN", "purpose": "deployment", "cidr": "192.168.200.0/24", "vlan_id": 5000}, http.StatusBadRequest, "")
	createdNetwork := requestJSON(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Test deployment network", "purpose": "deployment", "cidr": "192.168.200.0/24", "gateway": "192.168.200.1", "dns": "192.168.200.1", "vlan_id": 200, "dhcp_mode": "proxy", "proxy_dhcp": true, "status": "enabled"})
	if createdNetwork["cidr"] != "192.168.200.0/24" || createdNetwork["created_by"] != "admin@example.com" {
		t.Fatalf("expected created deployment network: %#v", createdNetwork)
	}
	createdNetworkID := intFromJSON(t, createdNetwork, "id")
	networkCheck := requestJSON(t, r, http.MethodPost, "/api/v1/network-configs/"+itoa(createdNetworkID)+"/check", token, map[string]any{})
	if networkCheck["status"] != "warning" || !jsonArrayContainsField(networkCheck["checks"].([]any), "name", "pxe_runtime") || !jsonArrayContainsField(networkCheck["checks"].([]any), "name", "deployment_usage") {
		t.Fatalf("expected deployment network check to report disabled PXE runtime warning: %#v", networkCheck)
	}
	alternateNetwork := requestJSON(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Alternate deployment network", "purpose": "deployment", "cidr": "192.168.201.0/24", "gateway": "192.168.201.1", "dns": "192.168.201.1", "vlan_id": 201, "dhcp_mode": "proxy", "proxy_dhcp": true, "status": "enabled"})
	alternateNetworkID := intFromJSON(t, alternateNetwork, "id")
	if alternateNetworkID == createdNetworkID {
		t.Fatalf("expected a distinct alternate network: %#v", alternateNetwork)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Overlapping deployment network", "purpose": "deployment", "cidr": "192.168.200.128/25", "gateway": "192.168.200.129", "status": "enabled"}, http.StatusBadRequest, "")
	disabledOverlap := requestJSON(t, r, http.MethodPost, "/api/v1/network-configs", token, map[string]any{"name": "Disabled overlapping deployment network", "purpose": "deployment", "cidr": "192.168.200.128/25", "gateway": "192.168.200.129", "status": "disabled"})
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/network-configs/"+itoa(intFromJSON(t, disabledOverlap, "id")), token, map[string]any{"status": "enabled"}, http.StatusBadRequest, "")

	preflightResp = requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": importedID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(preflightResp["problems"].([]any), "bmc endpoint not configured") {
		t.Fatalf("expected deployment preflight to require BMC endpoint: %#v", preflightResp)
	}
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/firmware", token, nil, http.StatusNotFound, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "endpoint": "https://bmc.test", "username": "admin", "password": "secret"}, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/999999/bmc", token, map[string]any{"type": "redfish", "endpoint": "https://bmc.test", "username": "admin", "password": "secret"}, http.StatusNotFound, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ilo", "endpoint": "https://bmc.test", "username": "admin", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "endpoint": "bmc.test", "username": "admin", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "protocol": "https", "endpoint": "http://bmc.test", "username": "admin", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "https", "endpoint": "192.0.2.55:623", "username": "ADMIN", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "ipmi", "endpoint": "bad host", "username": "ADMIN", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "ipmi", "endpoint": "192.0.2.55:notaport", "username": "ADMIN", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	bmcResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "endpoint": "https://bmc.test", "username": "admin", "password": "secret"}, "bmc.upsert")
	if intFromJSON(t, bmcResp, "server_id") != serverID {
		t.Fatalf("BMC endpoint was not bound to path server id")
	}
	if _, ok := bmcResp["password"]; ok {
		t.Fatalf("BMC response leaked plaintext password")
	}
	if bmcResp["encrypted_password_ref"] != "" {
		t.Fatalf("BMC response leaked credential reference: %#v", bmcResp)
	}
	if bmcResp["status"] != "unknown" {
		t.Fatalf("BMC endpoint should remain unknown until connectivity check succeeds: %#v", bmcResp)
	}
	var storedBMC models.BmcEndpoint
	if err := db.Where("server_id = ?", serverID).First(&storedBMC).Error; err != nil {
		t.Fatalf("expected stored BMC endpoint: %v", err)
	}
	originalBMCRef := storedBMC.EncryptedPasswordRef
	if originalBMCRef == "" {
		t.Fatalf("expected encrypted BMC credential reference")
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "endpoint": "https://bmc2.test", "username": "admin2", "encrypted_password_ref": "client-supplied"}, "bmc.upsert")
	if err := db.Where("server_id = ?", serverID).First(&storedBMC).Error; err != nil {
		t.Fatalf("expected updated BMC endpoint: %v", err)
	}
	if storedBMC.EncryptedPasswordRef != originalBMCRef {
		t.Fatalf("BMC credential reference was overwritten from request body: %#v", storedBMC)
	}
	if storedBMC.Status != "unknown" {
		t.Fatalf("BMC update should reset status to unknown before the next connectivity check: %#v", storedBMC)
	}
	deleteReferencedResp := requestJSONExpectStatus(t, r, http.MethodDelete, "/api/v1/servers/"+itoa(serverID), token, nil, http.StatusConflict, "server.delete")
	if !jsonArrayContainsField(deleteReferencedResp["blockers"].([]any), "name", "bmc_endpoints") {
		t.Fatalf("expected server delete to report BMC blocker: %#v", deleteReferencedResp)
	}

	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/power-on", token, map[string]any{}, "bmc.power-on")
	powerState := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/power", token, nil)
	if powerState["power_state"] != "on" || powerState["adapter"] != "simulated" {
		t.Fatalf("expected BMC power state on: %#v", powerState)
	}
	bmcCheck := requestJSON(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/check", token, map[string]any{})
	if bmcCheck["status"] != "ok" || bmcCheck["checked_at"] == nil {
		t.Fatalf("expected successful BMC connectivity check: %#v", bmcCheck)
	}
	bmcCheckProof, ok := bmcCheck["proof"].(map[string]any)
	if !ok || bmcCheckProof["adapter"] != "simulated" || bmcCheckProof["manufacturer"] != "Simulated" {
		t.Fatalf("expected successful BMC check to include non-secret identity proof: %#v", bmcCheck)
	}
	firmwareInfo := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/firmware", token, nil)
	if firmwareInfo["adapter"] != "simulated" || firmwareInfo["manufacturer"] != "Simulated" || firmwareInfo["bmc_version"] != "sim-bmc-1.0.0" {
		t.Fatalf("expected simulated BMC firmware info: %#v", firmwareInfo)
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/power-off", token, map[string]any{}, "bmc.power-off")
	powerState = requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/power", token, nil)
	if powerState["power_state"] != "off" {
		t.Fatalf("expected BMC power state off: %#v", powerState)
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/reboot", token, map[string]any{}, "bmc.reboot")
	powerState = requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/power", token, nil)
	if powerState["power_state"] != "on" {
		t.Fatalf("expected BMC reboot to leave power state on: %#v", powerState)
	}
	ipmiBMC := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(importedIPMIID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "ipmi", "endpoint": "192.0.2.55:623", "username": "ADMIN", "password": "secret"}, "bmc.upsert")
	if ipmiBMC["type"] != "ipmi" || ipmiBMC["protocol"] != "ipmi" {
		t.Fatalf("expected IPMI BMC endpoint to be saved: %#v", ipmiBMC)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/bmc/batch-power", token, map[string]any{"action": "power-off", "server_ids": []int{serverID, importedIPMIID}}, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/bmc/batch-power", token, map[string]any{"action": "power-off", "server_ids": []int{serverID, serverID}}, http.StatusBadRequest, "bmc.batch-power-off")
	batchPowerOff := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/bmc/batch-power", token, map[string]any{"action": "power-off", "server_ids": []int{serverID, importedIPMIID}}, "bmc.batch-power-off")
	if intFromJSON(t, batchPowerOff, "requested") != 2 || intFromJSON(t, batchPowerOff, "succeeded") != 2 || intFromJSON(t, batchPowerOff, "failed") != 0 {
		t.Fatalf("expected batch power-off to succeed for both BMC endpoints: %#v", batchPowerOff)
	}
	if statusCount(batchPowerOff["results"].([]any), "success") != 2 || !jsonArrayContainsField(batchPowerOff["results"].([]any), "power_state", "off") {
		t.Fatalf("expected successful batch power-off results: %#v", batchPowerOff)
	}
	batchPowerOn := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/bmc/batch-power", token, map[string]any{"action": "power-on", "server_ids": []int{serverID, importedID, terminalCandidateID, 999999}}, "bmc.batch-power-on")
	if intFromJSON(t, batchPowerOn, "requested") != 4 || intFromJSON(t, batchPowerOn, "succeeded") != 1 || intFromJSON(t, batchPowerOn, "failed") != 3 {
		t.Fatalf("expected mixed batch power-on results: %#v", batchPowerOn)
	}
	batchResults := batchPowerOn["results"].([]any)
	if !jsonArrayFieldContains(batchResults, "error", "bmc endpoint not found") || !jsonArrayFieldContains(batchResults, "error", "server not found") || !jsonArrayFieldContains(batchResults, "error", "retired or scrapped") {
		t.Fatalf("expected batch power-on to report per-target failures: %#v", batchPowerOn)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusBadRequest, "deployment.create")
	verified := requestJSON(t, r, http.MethodPost, "/api/v1/images/"+itoa(imageID)+"/verify", token, map[string]any{})
	if verified["test_status"] != "tested_passed" {
		t.Fatalf("expected image verification to pass: %#v", verified)
	}
	invalidNetworkResp := requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID, "network_id": 999999}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(invalidNetworkResp["problems"].([]any), "deployment network not found") {
		t.Fatalf("expected selected deployment network to be validated: %#v", invalidNetworkResp)
	}
	preflightResp = requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": importedID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(preflightResp["problems"].([]any), "bmc endpoint not configured") {
		t.Fatalf("expected verified deployment preflight to require BMC endpoint: %#v", preflightResp)
	}
	served := requestText(t, r, http.MethodGet, "/images/"+itoa(imageID)+"/file", "", nil)
	if served != "test image content" {
		t.Fatalf("unexpected served image content: %q", served)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, map[string]any{"server_id": serverID, "image_id": imageID, "template_id": 0, "workflow_id": workflowTemplateID}, http.StatusBadRequest, "deployment.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID, "variables": []string{"hostname"}}, http.StatusBadRequest, "deployment.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}, http.StatusBadRequest, "deployment.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID, "erase_policy": "unsafe"}), http.StatusBadRequest, "deployment.create")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusPreconditionRequired, "")
	cancelResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), "deployment.create")
	cancelDeploymentID := intFromJSON(t, cancelResp, "id")
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments/"+itoa(cancelDeploymentID)+"/cancel", token, map[string]any{}, "deployment.cancel")
	time.Sleep(1800 * time.Millisecond)
	cancelledDeployment := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(cancelDeploymentID), token, nil)
	if cancelledDeployment["status"] != "cancelled" {
		t.Fatalf("cancelled deployment was overwritten: %#v", cancelledDeployment)
	}
	requestJSON(t, r, http.MethodPatch, "/api/v1/servers/"+itoa(serverID), token, map[string]any{"status": "ready"})
	deploymentResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID, "network_id": createdNetworkID, "variables": map[string]any{"hostname": "test-node", "ssh_authorized_keys": []string{"ssh-ed25519 AAAATEST mvp"}}, "erase_policy": "full"}), "deployment.create")
	if deploymentResp["erase_policy"] != "full" || deploymentResp["erase_confirmed"] != true || deploymentResp["erase_confirmed_at"] == nil {
		t.Fatalf("expected deployment erase confirmation to be recorded: %#v", deploymentResp)
	}
	if intFromJSON(t, deploymentResp, "network_id") != createdNetworkID {
		t.Fatalf("expected deployment to record selected network %d: %#v", createdNetworkID, deploymentResp)
	}
	deploymentID := intFromJSON(t, deploymentResp, "id")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": serverID, "image_id": imageID, "template_id": installTemplateID, "workflow_id": workflowTemplateID}), http.StatusConflict, "deployment.create")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/images/"+itoa(imageID), token, nil, http.StatusConflict, "image.delete")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/install-templates/"+itoa(installTemplateID), token, nil, http.StatusConflict, "install_template.delete")
	requestExpectStatus(t, r, http.MethodDelete, "/api/v1/workflow-templates/"+itoa(workflowTemplateID), token, nil, http.StatusConflict, "workflow_template.delete")
	var deploymentMetadataToken models.MetadataToken
	if err := db.Where("deployment_id = ?", uint(deploymentID)).First(&deploymentMetadataToken).Error; err != nil {
		t.Fatalf("expected deployment metadata token: %v", err)
	}
	ipxe := requestText(t, r, http.MethodGet, "/boot/ipxe?mac=52:54:00:aa:bb:cc&arch=x86_64&firmware=uefi", "", nil)
	if !contains(ipxe, "#!ipxe") || !contains(ipxe, "image-url") || !contains(ipxe, "http://boot.test/images/"+itoa(imageID)+"/file") || !contains(ipxe, "http://boot.test/metadata/by-token/") {
		t.Fatalf("unexpected ipxe script: %s", ipxe)
	}
	metadataToken := metadataTokenFromIPXE(t, ipxe)
	if metadataToken != deploymentMetadataToken.Token {
		t.Fatalf("expected ipxe metadata token to match deployment token")
	}
	var deploymentBootEvent models.BootEvent
	if err := db.Where("deployment_id = ?", uint(deploymentID)).Order("id desc").First(&deploymentBootEvent).Error; err != nil {
		t.Fatalf("expected boot event for deployment: %v", err)
	}
	if contains(deploymentBootEvent.Script, metadataToken) || !contains(deploymentBootEvent.Script, "/metadata/by-token/<redacted>") {
		t.Fatalf("boot event script should redact metadata token: %s", deploymentBootEvent.Script)
	}
	installer := requestText(t, r, http.MethodGet, "/boot/linux-installer.ipxe", "", nil)
	if !contains(installer, "${image-url}") || !contains(installer, "${metadata-url}") {
		t.Fatalf("unexpected installer script: %s", installer)
	}
	discovery := requestText(t, r, http.MethodGet, "/boot/discovery.ipxe", "", nil)
	if !contains(discovery, "http://boot.test") {
		t.Fatalf("unexpected discovery script: %s", discovery)
	}

	hostname := requestText(t, r, http.MethodGet, "/metadata/by-server/"+itoa(serverID)+"/hostname", "", nil)
	if hostname != "test-node" {
		t.Fatalf("expected metadata hostname test-node, got %q", hostname)
	}
	tokenHostname := requestText(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken+"/hostname", "", nil)
	if tokenHostname != "test-node" {
		t.Fatalf("expected token metadata hostname test-node, got %q", tokenHostname)
	}
	tokenInstanceID := requestText(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken+"/instance-id", "", nil)
	if tokenInstanceID != "server-"+itoa(serverID) {
		t.Fatalf("expected token metadata instance id server-%d, got %q", serverID, tokenInstanceID)
	}
	macHostname := requestText(t, r, http.MethodGet, "/metadata/by-mac/52-54-00-AA-BB-CC/hostname", "", nil)
	if macHostname != "test-node" {
		t.Fatalf("expected MAC metadata hostname test-node, got %q", macHostname)
	}
	ipHostname := requestText(t, r, http.MethodGet, "/metadata/by-ip/192.168.200.50/hostname", "", nil)
	if ipHostname != "test-node" {
		t.Fatalf("expected IP metadata hostname test-node, got %q", ipHostname)
	}
	clientIPHostname := requestTextWithRemote(t, r, http.MethodGet, "/metadata/hostname", "", nil, "192.168.200.50:40123")
	if clientIPHostname != "test-node" {
		t.Fatalf("expected client IP metadata hostname test-node, got %q", clientIPHostname)
	}
	deploymentHostname := requestText(t, r, http.MethodGet, "/metadata/by-deployment/"+itoa(deploymentID)+"/hostname", "", nil)
	if deploymentHostname != "test-node" {
		t.Fatalf("expected deployment metadata hostname test-node, got %q", deploymentHostname)
	}
	sshKeys := requestJSON(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken+"/ssh-keys", "", nil)
	if !jsonArrayContainsString(sshKeys["keys"].([]any), "ssh-ed25519 AAAATEST mvp") {
		t.Fatalf("expected token metadata ssh key: %#v", sshKeys)
	}
	deploymentSSHKeys := requestJSON(t, r, http.MethodGet, "/metadata/by-deployment/"+itoa(deploymentID)+"/ssh-keys", "", nil)
	if !jsonArrayContainsString(deploymentSSHKeys["keys"].([]any), "ssh-ed25519 AAAATEST mvp") {
		t.Fatalf("expected deployment metadata ssh key: %#v", deploymentSSHKeys)
	}
	clientIPSSHKeys := requestTextWithRemote(t, r, http.MethodGet, "/metadata/ssh-keys", "", nil, "192.168.200.50:40123")
	if !contains(clientIPSSHKeys, "ssh-ed25519 AAAATEST mvp") {
		t.Fatalf("expected client IP metadata ssh key: %s", clientIPSSHKeys)
	}
	requestExpectStatus(t, r, http.MethodGet, "/metadata/by-deployment/"+itoa(deploymentID)+"/not-real", "", nil, http.StatusNotFound, "")
	tokenNetwork, _ := requestJSONWithHeaders(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken+"/network", "", nil, map[string]string{"X-Request-ID": "metadata-network-001"})
	if len(tokenNetwork["interfaces"].([]any)) != 1 {
		t.Fatalf("expected token metadata network interface: %#v", tokenNetwork)
	}
	tokenInterface := tokenNetwork["interfaces"].([]any)[0].(map[string]any)
	if int(tokenInterface["network_id"].(float64)) != createdNetworkID || tokenInterface["cidr"] != "192.168.200.0/24" || tokenInterface["gateway"] != "192.168.200.1" || int(tokenInterface["vlan_id"].(float64)) != 200 || tokenInterface["dhcp_mode"] != "proxy" {
		t.Fatalf("expected deployment network metadata: %#v", tokenNetwork)
	}
	if !jsonArrayContainsString(tokenInterface["dns"].([]any), "192.168.200.1") {
		t.Fatalf("expected deployment DNS metadata: %#v", tokenNetwork)
	}
	macNetwork := requestJSON(t, r, http.MethodGet, "/metadata/by-mac/52-54-00-aa-bb-cc/network", "", nil)
	macInterface := macNetwork["interfaces"].([]any)[0].(map[string]any)
	if int(macInterface["network_id"].(float64)) != createdNetworkID || macInterface["address"] != "192.168.200.50" {
		t.Fatalf("expected MAC metadata network to use selected deployment network and primary IP: %#v", macNetwork)
	}
	userdata := requestText(t, r, http.MethodGet, "/metadata/by-server/"+itoa(serverID)+"/userdata", "", nil)
	if !contains(userdata, "#cloud-config") || !contains(userdata, "hostname: test-node") {
		t.Fatalf("expected rendered cloud-config userdata, got %q", userdata)
	}
	tokenUserdata := requestText(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken+"/userdata", "", nil)
	if !contains(tokenUserdata, "#cloud-config") || !contains(tokenUserdata, "hostname: test-node") {
		t.Fatalf("expected rendered token cloud-config userdata, got %q", tokenUserdata)
	}
	ipUserdata := requestText(t, r, http.MethodGet, "/userdata/by-ip/192.168.200.50", "", nil)
	if !contains(ipUserdata, "#cloud-config") || !contains(ipUserdata, "hostname: test-node") {
		t.Fatalf("expected rendered IP userdata, got %q", ipUserdata)
	}
	clientIPUserdata := requestTextWithRemote(t, r, http.MethodGet, "/userdata", "", nil, "192.168.200.50:40123")
	if !contains(clientIPUserdata, "#cloud-config") || !contains(clientIPUserdata, "hostname: test-node") {
		t.Fatalf("expected rendered client IP userdata, got %q", clientIPUserdata)
	}
	requestExpectStatus(t, r, http.MethodGet, "/metadata/by-token/not-a-real-token/hostname", "", nil, http.StatusNotFound, "")
	metadataLogs := requestJSON(t, r, http.MethodGet, "/api/v1/log-events?server_id="+itoa(serverID)+"&source=metadata&page=1&page_size=20", token, nil)
	metadataLogItems := metadataLogs["items"].([]any)
	if intFromJSON(t, metadataLogs, "total") < 6 {
		t.Fatalf("expected metadata access logs: %#v", metadataLogs)
	}
	for _, needle := range []string{"endpoint=hostname", "endpoint=instance-id", "endpoint=network", "endpoint=ssh-keys", "endpoint=userdata", "mode=by-token", "mode=by-server", "mode=by-client-ip", "mode=by-mac", "mode=by-ip", "mode=by-deployment"} {
		if !jsonArrayFieldContains(metadataLogItems, "message", needle) {
			t.Fatalf("expected metadata log containing %q: %#v", needle, metadataLogs)
		}
	}
	if !jsonArrayContainsField(metadataLogItems, "trace_id", "metadata-network-001") {
		t.Fatalf("expected metadata access log to preserve request id: %#v", metadataLogs)
	}
	if jsonArrayFieldContains(metadataLogItems, "message", metadataToken) || jsonArrayFieldContains(metadataLogItems, "trace_id", metadataToken) {
		t.Fatalf("metadata access logs leaked metadata token: %#v", metadataLogs)
	}
	filteredDeployments := requestJSON(t, r, http.MethodGet, "/api/v1/deployments?server_id="+itoa(serverID)+"&requested_by=admin@example.com&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredDeployments, "total") < 2 || intFromJSON(t, filteredDeployments, "page_size") != 1 {
		t.Fatalf("expected filtered deployment pagination: %#v", filteredDeployments)
	}
	filteredDeploymentItems := filteredDeployments["items"].([]any)
	if len(filteredDeploymentItems) != 1 || int(filteredDeploymentItems[0].(map[string]any)["server_id"].(float64)) != serverID {
		t.Fatalf("expected filtered deployment row: %#v", filteredDeployments)
	}

	time.Sleep(1800 * time.Millisecond)
	logs := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentID)+"/logs", token, nil)
	tasks, ok := logs["tasks"].([]any)
	if !ok || len(tasks) != 4 {
		t.Fatalf("expected 4 workflow tasks, got %#v", logs["tasks"])
	}
	logSummary := logs["summary"].(map[string]any)
	if intFromJSON(t, logSummary, "total_runs") != 1 || intFromJSON(t, logSummary, "task_total") != 4 || intFromJSON(t, logSummary, "task_success") != 4 {
		t.Fatalf("expected workflow log summary with task counts: %#v", logs)
	}
	logRuns := logs["runs"].([]any)
	if len(logRuns) != 1 || int(logRuns[0].(map[string]any)["attempt"].(float64)) != 1 {
		t.Fatalf("expected workflow run history: %#v", logs)
	}
	if _, ok := tasks[0].(map[string]any)["duration_ms"].(float64); !ok {
		t.Fatalf("expected task duration_ms in deployment logs: %#v", tasks[0])
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": 0, "reason": "invalid"}, http.StatusBadRequest, "ops.terminal.open")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": 999999, "reason": "missing"}, http.StatusBadRequest, "ops.terminal.open")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": serverID, "reason": strings.Repeat("x", 256)}, http.StatusBadRequest, "ops.terminal.open")
	terminal := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": serverID, "reason": "break-glass inspection"}, "ops.terminal.open")
	terminalID := intFromJSON(t, terminal, "id")
	if terminal["status"] != "active" || terminal["mode"] != "simulated" {
		t.Fatalf("unexpected terminal session response: %#v", terminal)
	}
	if terminal["server_id"].(float64) != float64(serverID) {
		t.Fatalf("terminal session not bound to server: %#v", terminal)
	}
	terminalDetail := requestJSON(t, r, http.MethodGet, "/api/v1/ops/terminal-sessions/"+itoa(terminalID), token, nil)
	if !contains(terminalDetail["transcript"].(string), "Simulated terminal session opened") {
		t.Fatalf("expected simulated terminal transcript: %#v", terminalDetail)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions/not-a-number/close", token, map[string]any{}, http.StatusBadRequest, "ops.terminal.close")
	closedTerminal := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions/"+itoa(terminalID)+"/close", token, map[string]any{}, "ops.terminal.close")
	if closedTerminal["status"] != "closed" {
		t.Fatalf("expected terminal session to close: %#v", closedTerminal)
	}
	filteredTerminals := requestJSON(t, r, http.MethodGet, "/api/v1/ops/terminal-sessions?server_id="+itoa(serverID)+"&status=closed&mode=simulated&requested_by=admin@example.com&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredTerminals, "total") != 1 || intFromJSON(t, filteredTerminals, "page_size") != 1 {
		t.Fatalf("expected filtered terminal pagination: %#v", filteredTerminals)
	}
	filteredTerminalItems := filteredTerminals["items"].([]any)
	if len(filteredTerminalItems) != 1 || int(filteredTerminalItems[0].(map[string]any)["server_id"].(float64)) != serverID {
		t.Fatalf("expected filtered terminal row: %#v", filteredTerminals)
	}
	backup := requestJSONWithConfirm(t, r, http.MethodGet, "/api/v1/ops/backup/export", token, nil, "ops.backup.export")
	if backup["version"] != "mvp-v0.1" {
		t.Fatalf("expected backup version: %#v", backup)
	}
	if len(backup["servers"].([]any)) == 0 || len(backup["network_configs"].([]any)) == 0 || len(backup["audit_logs"].([]any)) == 0 {
		t.Fatalf("expected backup collections: %#v", backup)
	}
	if len(backup["alert_rules"].([]any)) == 0 {
		t.Fatalf("expected backup to include alert rules: %#v", backup)
	}
	if _, ok := backup["retirement_records"].([]any); !ok {
		t.Fatalf("expected backup to include retirement records section: %#v", backup)
	}
	if len(backup["lab_validation_runs"].([]any)) == 0 || len(backup["lab_validation_run_results"].([]any)) == 0 {
		t.Fatalf("expected backup to include lab validation run history: %#v", backup)
	}
	backupValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, backup)
	if backupValidation["status"] == "error" {
		t.Fatalf("expected exported backup to pass validation without errors: %#v", backupValidation)
	}
	validationChecks := backupValidation["checks"].([]any)
	if !jsonArrayContainsField(validationChecks, "name", "references") || jsonArrayContainsField(validationChecks, "status", "error") {
		t.Fatalf("expected backup validation reference checks without errors: %#v", backupValidation)
	}
	invalidBackup := copyMap(backup)
	invalidBackup["version"] = "unsupported"
	invalidBackupValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, invalidBackup)
	if invalidBackupValidation["status"] != "error" {
		t.Fatalf("expected unsupported backup version to fail validation: %#v", invalidBackupValidation)
	}
	duplicateIdentityBackup := cloneJSONMap(t, backup)
	duplicateIdentityBackup["servers"] = append(duplicateIdentityBackup["servers"].([]any), map[string]any{"id": 999001, "asset_no": "BM-T-001", "hostname": "backup-duplicate-identity", "status": "ready", "architecture": "x86_64", "tenant_id": "default"})
	duplicateIdentityValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, duplicateIdentityBackup)
	if duplicateIdentityValidation["status"] != "error" || !jsonArrayFieldContains(duplicateIdentityValidation["checks"].([]any), "message", "servers.asset_no contains duplicate") {
		t.Fatalf("expected duplicate server identity to fail backup validation: %#v", duplicateIdentityValidation)
	}
	overlappingNetworkBackup := cloneJSONMap(t, backup)
	overlappingNetworkBackup["network_configs"] = append(overlappingNetworkBackup["network_configs"].([]any), map[string]any{"id": 999002, "name": "Backup overlapping deployment network", "purpose": "deployment", "cidr": "192.168.200.128/25", "gateway": "192.168.200.129", "dhcp_mode": "proxy", "status": "enabled"})
	overlappingNetworkValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, overlappingNetworkBackup)
	if overlappingNetworkValidation["status"] != "error" || !jsonArrayFieldContains(overlappingNetworkValidation["checks"].([]any), "message", "overlaps with network_config") {
		t.Fatalf("expected overlapping network to fail backup validation: %#v", overlappingNetworkValidation)
	}
	missingNetworkBackup := cloneJSONMap(t, backup)
	missingDeployments := missingNetworkBackup["deployments"].([]any)
	if len(missingDeployments) == 0 {
		t.Fatalf("expected backup deployments for network reference validation: %#v", missingNetworkBackup)
	}
	missingDeployments[0].(map[string]any)["network_id"] = 999999
	missingNetworkValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, missingNetworkBackup)
	if missingNetworkValidation["status"] != "error" || !jsonArrayFieldContains(missingNetworkValidation["checks"].([]any), "message", "references missing network_config") {
		t.Fatalf("expected missing deployment network to fail backup validation: %#v", missingNetworkValidation)
	}
	quotaBackup := cloneJSONMap(t, backup)
	for _, item := range quotaBackup["tenants"].([]any) {
		tenant := item.(map[string]any)
		if tenant["tenant_id"] == "tenant-quota" {
			tenant["quota"] = map[string]any{"servers": 0}
		}
	}
	quotaBackupValidation := requestJSON(t, r, http.MethodPost, "/api/v1/ops/backup/validate", token, quotaBackup)
	if quotaBackupValidation["status"] != "error" || !jsonArrayFieldContains(quotaBackupValidation["checks"].([]any), "message", "server quota exceeded in backup") {
		t.Fatalf("expected tenant quota usage to fail backup validation: %#v", quotaBackupValidation)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/backup/restore", token, backup, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/backup/restore", token, backup, http.StatusConflict, "ops.backup.restore")

	dirtyRestoreCfg := cfg
	dirtyRestoreCfg.DatabaseURL = filepath.Join(t.TempDir(), "dirty-restore.db")
	dirtyRestoreCfg.ImageStorageDir = t.TempDir()
	dirtyRestoreCfg.EnableDemoSeeder = false
	dirtyRestoreDB, err := database.Connect(dirtyRestoreCfg)
	if err != nil {
		t.Fatalf("dirty restore target database init: %v", err)
	}
	dirtyRestoreSQL, err := dirtyRestoreDB.DB()
	if err != nil {
		t.Fatalf("dirty restore target sql handle: %v", err)
	}
	defer dirtyRestoreSQL.Close()
	if err := dirtyRestoreDB.Create(&models.BootEvent{MAC: "52:54:00:ee:00:01", Architecture: "x86_64", RemoteAddr: "127.0.0.1"}).Error; err != nil {
		t.Fatalf("seed dirty restore target: %v", err)
	}
	dirtyRestoreRouter := NewRouter(dirtyRestoreDB, dirtyRestoreCfg)
	dirtyRestoreToken := loginForTest(t, dirtyRestoreRouter)
	requestExpectStatus(t, dirtyRestoreRouter, http.MethodPost, "/api/v1/ops/backup/restore", dirtyRestoreToken, backup, http.StatusConflict, "ops.backup.restore")

	restoreCfg := cfg
	restoreCfg.DatabaseURL = filepath.Join(t.TempDir(), "restore.db")
	restoreCfg.ImageStorageDir = t.TempDir()
	restoreCfg.EnableDemoSeeder = false
	restoreDB, err := database.Connect(restoreCfg)
	if err != nil {
		t.Fatalf("restore target database init: %v", err)
	}
	restoreSQL, err := restoreDB.DB()
	if err != nil {
		t.Fatalf("restore target sql handle: %v", err)
	}
	defer restoreSQL.Close()
	restoreRouter := NewRouter(restoreDB, restoreCfg)
	restoreToken := loginForTest(t, restoreRouter)
	restoreResp := requestJSONWithConfirm(t, restoreRouter, http.MethodPost, "/api/v1/ops/backup/restore", restoreToken, backup, "ops.backup.restore")
	if restoreResp["status"] != "restored" || intFromJSON(t, restoreResp["imported"].(map[string]any), "servers") == 0 {
		t.Fatalf("expected backup restore result: %#v", restoreResp)
	}
	if intFromJSON(t, restoreResp["imported"].(map[string]any), "lab_validation_runs") == 0 {
		t.Fatalf("expected backup restore to import lab validation runs: %#v", restoreResp)
	}
	restoredLoginToken := loginForTest(t, restoreRouter)
	if restoredLoginToken == "" {
		t.Fatalf("expected restored admin to remain able to log in")
	}
	restoredServers := requestJSON(t, restoreRouter, http.MethodGet, "/api/v1/servers?keyword=test-node&page=1&page_size=1", restoredLoginToken, nil)
	if intFromJSON(t, restoredServers, "total") != 1 {
		t.Fatalf("expected restored server data: %#v", restoredServers)
	}
	maxRestoredServerID := 0
	for _, rawServer := range backup["servers"].([]any) {
		id := int(rawServer.(map[string]any)["id"].(float64))
		if id > maxRestoredServerID {
			maxRestoredServerID = id
		}
	}
	postRestoreServer := requestJSON(t, restoreRouter, http.MethodPost, "/api/v1/servers", restoredLoginToken, map[string]any{"asset_no": "BM-POST-RESTORE", "hostname": "post-restore", "primary_mac": "52:54:00:ee:00:02", "tenant_id": "default"})
	if intFromJSON(t, postRestoreServer, "id") <= maxRestoredServerID {
		t.Fatalf("expected post-restore server id to advance past restored ids: %#v", postRestoreServer)
	}
	restoredAudits := requestJSON(t, restoreRouter, http.MethodGet, "/api/v1/audit-logs?action=ops.backup.restore&page=1&page_size=1", restoredLoginToken, nil)
	if intFromJSON(t, restoredAudits, "total") != 1 {
		t.Fatalf("expected restore audit record: %#v", restoredAudits)
	}

	alerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts", token, nil)
	alertRows := alerts["_array"].([]any)
	if len(alertRows) == 0 {
		t.Fatalf("expected demo alert")
	}
	alert := alertRows[0].(map[string]any)
	alertID := int(alert["id"].(float64))
	filteredAlerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(int(alert["server_id"].(float64)))+"&severity=warning&status=firing&rule_id=disk.smart.warning&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredAlerts, "total") != 1 || intFromJSON(t, filteredAlerts, "page_size") != 1 {
		t.Fatalf("expected filtered alert pagination: %#v", filteredAlerts)
	}
	filteredAlertItems := filteredAlerts["items"].([]any)
	if len(filteredAlertItems) != 1 || filteredAlertItems[0].(map[string]any)["title"] != "Demo SMART warning" {
		t.Fatalf("expected filtered alert row: %#v", filteredAlerts)
	}
	logEvents := requestJSON(t, r, http.MethodGet, "/api/v1/log-events?server_id="+itoa(int(alert["server_id"].(float64)))+"&source=agentless&level=warning&keyword=latency&page=1&page_size=1", token, nil)
	if intFromJSON(t, logEvents, "total") != 1 || intFromJSON(t, logEvents, "page_size") != 1 {
		t.Fatalf("expected filtered log event pagination: %#v", logEvents)
	}
	logEventItems := logEvents["items"].([]any)
	if len(logEventItems) != 1 || logEventItems[0].(map[string]any)["trace_id"] != "demo-log-0001" {
		t.Fatalf("expected filtered log event row: %#v", logEvents)
	}
	alertRules := requestJSON(t, r, http.MethodGet, "/api/v1/alert-rules?status=enabled&severity=warning&metric_name=cpu_usage&keyword=CPU&page=1&page_size=1", token, nil)
	if intFromJSON(t, alertRules, "total") != 1 || intFromJSON(t, alertRules, "page_size") != 1 {
		t.Fatalf("expected filtered alert rule pagination: %#v", alertRules)
	}
	alertRuleItems := alertRules["items"].([]any)
	if len(alertRuleItems) != 1 || alertRuleItems[0].(map[string]any)["rule_id"] != "cpu.high" {
		t.Fatalf("expected filtered alert rule row: %#v", alertRules)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alert-rules", token, map[string]any{"rule_id": "bad.operator", "name": "Bad Operator", "metric_name": "cpu_usage", "operator": "!=", "threshold": 90}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alert-rules", token, map[string]any{"rule_id": "bad metric", "name": "Bad Metric", "metric_name": "cpu usage", "operator": ">", "threshold": 90}, http.StatusBadRequest, "")
	customAlertRule := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules", token, map[string]any{"rule_id": "test.cpu.low", "name": "Test CPU low", "metric_name": "cpu_usage", "operator": "<", "threshold": 1, "severity": "info", "status": "disabled"})
	customAlertRuleID := intFromJSON(t, customAlertRule, "id")
	requestExpectStatus(t, r, http.MethodPatch, "/api/v1/alert-rules/"+itoa(customAlertRuleID), token, map[string]any{"status": "paused"}, http.StatusBadRequest, "")
	updatedAlertRule := requestJSON(t, r, http.MethodPatch, "/api/v1/alert-rules/"+itoa(customAlertRuleID), token, map[string]any{"description": "validated by test", "threshold": 2})
	if updatedAlertRule["description"] != "validated by test" || int(updatedAlertRule["threshold"].(float64)) != 2 {
		t.Fatalf("expected validated alert rule update: %#v", updatedAlertRule)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/ack", token, map[string]any{"note": strings.Repeat("x", 1001)}, http.StatusBadRequest, "")
	ackedAlert := requestJSON(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/ack", token, map[string]any{"note": "investigating SMART warning"})
	if ackedAlert["status"] != "acknowledged" || ackedAlert["acknowledged_by"] != "admin@example.com" {
		t.Fatalf("expected acknowledged alert with handler: %#v", ackedAlert)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/ack", token, map[string]any{"note": "duplicate ack"}, http.StatusBadRequest, "")
	resolvedAlert := requestJSON(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/resolve", token, map[string]any{"note": "disk replaced"})
	if resolvedAlert["status"] != "resolved" || resolvedAlert["resolved_by"] != "admin@example.com" {
		t.Fatalf("expected resolved alert with handler: %#v", resolvedAlert)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/resolve", token, map[string]any{"note": "duplicate resolve"}, http.StatusBadRequest, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/ack", token, map[string]any{"note": "late ack"}, http.StatusBadRequest, "")
	alertEvents := requestJSON(t, r, http.MethodGet, "/api/v1/alerts/"+itoa(alertID)+"/events", token, nil)
	eventRows := alertEvents["_array"].([]any)
	if len(eventRows) != 2 {
		t.Fatalf("expected alert handling events: %#v", alertEvents)
	}
	if eventRows[0].(map[string]any)["actor_email"] != "admin@example.com" {
		t.Fatalf("expected alert event actor: %#v", alertEvents)
	}

	dashboard := requestJSON(t, r, http.MethodGet, "/api/v1/dashboard", token, nil)
	if intFromJSON(t, dashboard, "servers") == 0 || intFromJSON(t, dashboard, "deployments") == 0 {
		t.Fatalf("expected dashboard counters: %#v", dashboard)
	}
	if _, ok := dashboard["server_statuses"].(map[string]any); !ok {
		t.Fatalf("expected server status breakdown: %#v", dashboard)
	}
	if _, ok := dashboard["deployment_statuses"].(map[string]any); !ok {
		t.Fatalf("expected deployment status breakdown: %#v", dashboard)
	}
	if _, ok := dashboard["alert_severities"].(map[string]any); !ok {
		t.Fatalf("expected alert severity breakdown: %#v", dashboard)
	}
	if len(dashboard["recent_deployments"].([]any)) == 0 {
		t.Fatalf("expected recent deployments: %#v", dashboard)
	}
	if len(dashboard["recent_audit_logs"].([]any)) == 0 {
		t.Fatalf("expected recent audit logs: %#v", dashboard)
	}

	audits := requestJSON(t, r, http.MethodGet, "/api/v1/audit-logs", token, nil)
	if len(audits["_array"].([]any)) < 9 {
		t.Fatalf("expected audit records")
	}
	if jsonArrayContainsBlankField(audits["_array"].([]any), "request_id") {
		t.Fatalf("expected all audit records to include request_id: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "user.create") || !jsonArrayContainsAction(audits["_array"].([]any), "user.update") || !jsonArrayContainsAction(audits["_array"].([]any), "user.password.reset") {
		t.Fatalf("expected user management audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "tenant.create") || !jsonArrayContainsAction(audits["_array"].([]any), "tenant.update") {
		t.Fatalf("expected tenant management audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "network_config.create") || !jsonArrayContainsAction(audits["_array"].([]any), "network_config.update") || !jsonArrayContainsAction(audits["_array"].([]any), "network_config.check") {
		t.Fatalf("expected network config audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "install_template.update") || !jsonArrayContainsAction(audits["_array"].([]any), "workflow_template.update") {
		t.Fatalf("expected template update audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "image.upload") {
		t.Fatalf("expected image upload audit record: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "bmc.power-on") || !jsonArrayContainsAction(audits["_array"].([]any), "bmc.power-off") || !jsonArrayContainsAction(audits["_array"].([]any), "bmc.reboot") || !jsonArrayContainsAction(audits["_array"].([]any), "bmc.check") || !jsonArrayContainsAction(audits["_array"].([]any), "bmc.firmware.read") {
		t.Fatalf("expected BMC audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "bmc.batch-power-on") || !jsonArrayContainsAction(audits["_array"].([]any), "bmc.batch-power-off") {
		t.Fatalf("expected batch BMC audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "alert_rule.create") || !jsonArrayContainsAction(audits["_array"].([]any), "alert_rule.update") {
		t.Fatalf("expected alert rule audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "ops.script.create") {
		t.Fatalf("expected script job audit record: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "ops.logs.collect") {
		t.Fatalf("expected log collection audit record: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "ops.terminal.open") || !jsonArrayContainsAction(audits["_array"].([]any), "ops.terminal.close") {
		t.Fatalf("expected terminal session audit records: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "ops.backup.export") {
		t.Fatalf("expected backup export audit record: %#v", audits)
	}
	if !jsonArrayContainsAction(audits["_array"].([]any), "ops.backup.validate") {
		t.Fatalf("expected backup validation audit record: %#v", audits)
	}
	filteredAudits := requestJSON(t, r, http.MethodGet, "/api/v1/audit-logs?action=ops.terminal.open&risk_level=high&page=1&page_size=1", token, nil)
	if intFromJSON(t, filteredAudits, "page") != 1 || intFromJSON(t, filteredAudits, "page_size") != 1 {
		t.Fatalf("expected audit pagination metadata: %#v", filteredAudits)
	}
	if intFromJSON(t, filteredAudits, "total") != 1 {
		t.Fatalf("expected one filtered audit record: %#v", filteredAudits)
	}
	filteredItems := filteredAudits["items"].([]any)
	if len(filteredItems) != 1 || filteredItems[0].(map[string]any)["action"] != "ops.terminal.open" {
		t.Fatalf("expected filtered audit item: %#v", filteredAudits)
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/999999/ssh", token, map[string]any{"host": "192.0.2.10", "port": 22, "username": "root", "secret": "ssh-password"}, http.StatusNotFound, "ssh.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "bad host", "port": 22, "username": "root", "secret": "ssh-password"}, http.StatusBadRequest, "ssh.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "192.0.2.10", "port": 70000, "username": "root", "secret": "ssh-password"}, http.StatusBadRequest, "ssh.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "192.0.2.10", "port": 22, "username": "root", "auth_type": "token", "secret": "ssh-password"}, http.StatusBadRequest, "ssh.upsert")
	ssh := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "192.0.2.10", "port": 22, "username": "root", "secret": "ssh-password"}, "ssh.upsert")
	if intFromJSON(t, ssh, "server_id") != serverID {
		t.Fatalf("SSH access not bound to server")
	}
	if _, ok := ssh["secret"]; ok {
		t.Fatalf("SSH response leaked secret")
	}
	if ssh["credential_ref"] != "" {
		t.Fatalf("SSH response leaked credential reference: %#v", ssh)
	}
	var storedSSH models.SSHAccess
	if err := db.Where("server_id = ?", serverID).First(&storedSSH).Error; err != nil {
		t.Fatalf("expected stored SSH access: %v", err)
	}
	originalSSHRef := storedSSH.CredentialRef
	if originalSSHRef == "" {
		t.Fatalf("expected encrypted SSH credential reference")
	}
	updatedSSH := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "192.0.2.11", "port": 22, "username": "root2", "credential_ref": "client-supplied"}, "ssh.upsert")
	if updatedSSH["credential_ref"] != "" {
		t.Fatalf("SSH update response leaked credential reference: %#v", updatedSSH)
	}
	if err := db.Where("server_id = ?", serverID).First(&storedSSH).Error; err != nil {
		t.Fatalf("expected updated SSH access: %v", err)
	}
	if storedSSH.CredentialRef != originalSSHRef {
		t.Fatalf("SSH credential reference was overwritten from request body: %#v", storedSSH)
	}
	labWithEndpoints := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	bmcSummary, ok := labWithEndpoints["bmc"].(map[string]any)
	if !ok || intFromJSON(t, bmcSummary, "total") < 2 {
		t.Fatalf("expected lab validation to summarize configured BMC endpoints: %#v", labWithEndpoints)
	}
	sshSummary, ok := labWithEndpoints["ssh"].(map[string]any)
	if !ok || intFromJSON(t, sshSummary, "total") < 1 {
		t.Fatalf("expected lab validation to summarize configured SSH access: %#v", labWithEndpoints)
	}
	targetedLabRun := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/run", token, map[string]any{"check_pxe": false, "check_bmc": true, "check_ssh": false, "server_ids": []int{serverID}}, "system.lab-validation.run")
	runResults, ok := targetedLabRun["run_results"].([]any)
	if !ok || !jsonArrayContainsField(runResults, "kind", "bmc") || !jsonArrayContainsField(runResults, "server_id", float64(serverID)) {
		t.Fatalf("expected targeted lab run to include requested BMC server result: %#v", targetedLabRun)
	}
	labAfterTargetedRun := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	target, ok := jsonArrayFindByField(labAfterTargetedRun["targets"].([]any), "server_id", float64(serverID))
	if !ok {
		t.Fatalf("expected target matrix to include targeted lab run server: %#v", labAfterTargetedRun)
	}
	if intFromJSON(t, target, "latest_run_id") != intFromJSON(t, targetedLabRun, "run_id") || target["latest_run_status"] != targetedLabRun["status"] || target["latest_run_kind"] != "bmc" {
		t.Fatalf("expected target matrix to reference latest lab run: target=%#v run=%#v", target, targetedLabRun)
	}
	if target["latest_run_result_status"] == "" || target["latest_run_at"] == "" {
		t.Fatalf("expected target matrix to include latest run result status/time: %#v", target)
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/999999/collections", token, map[string]any{}, http.StatusNotFound, "")
	job := requestJSON(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/collections", token, map[string]any{})
	if intFromJSON(t, job, "server_id") != serverID {
		t.Fatalf("collection job not bound to server")
	}
	collectionJobs := requestJSON(t, r, http.MethodGet, "/api/v1/collections?server_id="+itoa(serverID)+"&status=running&mode=ssh_agentless&requested_by=admin@example.com&page=1&page_size=1", token, nil)
	if intFromJSON(t, collectionJobs, "total") != 1 || intFromJSON(t, collectionJobs, "page_size") != 1 {
		t.Fatalf("expected filtered collection job pagination: %#v", collectionJobs)
	}
	collectionItems := collectionJobs["items"].([]any)
	if len(collectionItems) != 1 || int(collectionItems[0].(map[string]any)["server_id"].(float64)) != serverID {
		t.Fatalf("expected filtered collection job row: %#v", collectionJobs)
	}
	time.Sleep(500 * time.Millisecond)
	requestExpectStatus(t, r, http.MethodGet, "/api/v1/servers/999999/metrics", token, nil, http.StatusNotFound, "")
	metrics := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/metrics", token, nil)
	if len(metrics["_array"].([]any)) == 0 {
		t.Fatalf("expected collected metrics")
	}
	if int(metrics["_array"].([]any)[0].(map[string]any)["server_id"].(float64)) != serverID {
		t.Fatalf("expected metrics bound to server: %#v", metrics)
	}
	for _, metric := range []string{"host_up", "cpu_usage", "memory_usage", "disk_usage", "disk_smart_health", "network_rx_mbps", "network_tx_mbps", "process_count", "process_zombie_count"} {
		if !jsonArrayContainsField(metrics["_array"].([]any), "metric_name", metric) {
			t.Fatalf("expected metric %s in collected metrics: %#v", metric, metrics)
		}
	}
	now := time.Now().UTC()
	db.Create(&[]models.MetricSample{
		{ServerID: uint(serverID), MetricName: "memory_usage", Value: 91, Unit: "%", CollectedAt: now.Add(1 * time.Second)},
		{ServerID: uint(serverID), MetricName: "disk_usage", Value: 96, Unit: "%", CollectedAt: now.Add(2 * time.Second)},
		{ServerID: uint(serverID), MetricName: "host_up", Value: 0, Unit: "bool", CollectedAt: now.Add(3 * time.Second)},
		{ServerID: uint(serverID), MetricName: "disk_smart_health", Value: 1, Unit: "bool", CollectedAt: now.Add(4 * time.Second)},
	})
	evaluated := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules/evaluate", token, map[string]any{})
	if intFromJSON(t, evaluated, "created") < 5 {
		t.Fatalf("expected alert rule evaluation to create alerts: %#v", evaluated)
	}
	for _, ruleID := range []string{"cpu.high", "memory.high", "disk.full", "host.offline", "disk.smart.warning"} {
		ruleAlerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(serverID)+"&rule_id="+ruleID+"&status=firing&page=1&page_size=1", token, nil)
		if intFromJSON(t, ruleAlerts, "total") != 1 {
			t.Fatalf("expected %s alert from rule evaluation: %#v", ruleID, ruleAlerts)
		}
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/retire", token, map[string]any{"erase_status": "unknown"}, http.StatusBadRequest, "server.retire")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/retire", token, map[string]any{"erase_status": "verified", "erase_method": "nvme sanitize"}, http.StatusBadRequest, "server.retire")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/retire", token, map[string]any{"reason": strings.Repeat("x", 501)}, http.StatusBadRequest, "server.retire")
	retireResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/retire", token, map[string]any{"reason": "hardware refresh", "erase_status": "verified", "erase_method": "nvme sanitize", "evidence": "SEC-123 verified wipe"}, "server.retire")
	retirement := retireResp["retirement"].(map[string]any)
	if retirement["reason"] != "hardware refresh" || retirement["erase_status"] != "verified" || retirement["erase_method"] != "nvme sanitize" {
		t.Fatalf("expected retirement record in retire response: %#v", retireResp)
	}
	retirementRecords := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/retirement-records", token, nil)
	retirementRows := retirementRecords["_array"].([]any)
	if len(retirementRows) != 1 {
		t.Fatalf("expected one retirement record: %#v", retirementRecords)
	}
	if retirementRows[0].(map[string]any)["evidence"] != "SEC-123 verified wipe" {
		t.Fatalf("expected retirement evidence to be queryable: %#v", retirementRows[0])
	}
	lifecycleLogs := requestJSON(t, r, http.MethodGet, "/api/v1/log-events?server_id="+itoa(serverID)+"&source=lifecycle&page=1&page_size=10", token, nil)
	if intFromJSON(t, lifecycleLogs, "total") < 1 || !jsonArrayFieldContains(lifecycleLogs["items"].([]any), "message", "hardware refresh") {
		t.Fatalf("expected lifecycle log for retirement: %#v", lifecycleLogs)
	}
	retiredPowerState := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/power", token, nil)
	if retiredPowerState["adapter"] != "simulated" {
		t.Fatalf("expected retired server power state to remain readable: %#v", retiredPowerState)
	}
	retiredFirmwareInfo := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/bmc/firmware", token, nil)
	if retiredFirmwareInfo["adapter"] != "simulated" {
		t.Fatalf("expected retired server firmware info to remain readable: %#v", retiredFirmwareInfo)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "redfish", "endpoint": "https://bmc-retired.test", "username": "admin", "password": "secret"}, http.StatusConflict, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/check", token, map[string]any{}, http.StatusConflict, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/power-on", token, map[string]any{}, http.StatusConflict, "bmc.power-on")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/power-off", token, map[string]any{}, http.StatusConflict, "bmc.power-off")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc/reboot", token, map[string]any{}, http.StatusConflict, "bmc.reboot")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": serverID, "reason": "retired server"}, http.StatusBadRequest, "ops.terminal.open")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/ssh", token, map[string]any{"host": "192.0.2.10", "port": 22, "username": "root", "secret": "ssh-password"}, http.StatusBadRequest, "ssh.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/collections", token, map[string]any{}, http.StatusBadRequest, "")
	history = requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	historyRows := history["_array"].([]any)
	if len(historyRows) < 4 {
		t.Fatalf("expected lifecycle history through retire: %#v", history)
	}
	lastHistory := historyRows[0].(map[string]any)
	if lastHistory["to_status"] != "retired" {
		t.Fatalf("expected latest history to be retired: %#v", lastHistory)
	}
	if !strings.Contains(lastHistory["reason"].(string), "hardware refresh") {
		t.Fatalf("expected status history reason to include retirement reason: %#v", lastHistory)
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/retire", token, map[string]any{}, "server.retire")
	repeatedRetireHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	if len(repeatedRetireHistory["_array"].([]any)) != len(historyRows) {
		t.Fatalf("repeated retire should not create extra history: before=%#v after=%#v", history, repeatedRetireHistory)
	}
	repeatedRetirementRecords := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/retirement-records", token, nil)
	if len(repeatedRetirementRecords["_array"].([]any)) != len(retirementRows) {
		t.Fatalf("repeated retire should not create extra retirement record: before=%#v after=%#v", retirementRecords, repeatedRetirementRecords)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/scrap", token, map[string]any{"erase_status": "verified", "erase_method": "shred"}, http.StatusBadRequest, "server.scrap")
	scrapResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/scrap", token, map[string]any{"reason": "asset destroyed", "erase_status": "failed", "erase_method": "drive shredder", "evidence": "destruction certificate pending"}, "server.scrap")
	if scrapResp["status"] != "scrapped" {
		t.Fatalf("expected server scrap response: %#v", scrapResp)
	}
	scrapRecord := scrapResp["retirement"].(map[string]any)
	if scrapRecord["to_status"] != "scrapped" || scrapRecord["reason"] != "asset destroyed" || scrapRecord["erase_status"] != "failed" {
		t.Fatalf("expected scrap retirement record: %#v", scrapResp)
	}
	scrapHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	scrapHistoryRows := scrapHistory["_array"].([]any)
	if len(scrapHistoryRows) != len(historyRows)+1 {
		t.Fatalf("expected one extra status history for scrap: before=%#v after=%#v", history, scrapHistory)
	}
	if latest := scrapHistoryRows[0].(map[string]any); latest["to_status"] != "scrapped" || !strings.Contains(latest["reason"].(string), "asset destroyed") {
		t.Fatalf("expected latest history to be scrapped with reason: %#v", latest)
	}
	scrapRecords := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/retirement-records", token, nil)
	scrapRows := scrapRecords["_array"].([]any)
	if len(scrapRows) != len(retirementRows)+1 {
		t.Fatalf("expected scrap to append one terminal record: before=%#v after=%#v", retirementRecords, scrapRecords)
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/scrap", token, map[string]any{}, "server.scrap")
	repeatedScrapHistory := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/status-history", token, nil)
	if len(repeatedScrapHistory["_array"].([]any)) != len(scrapHistoryRows) {
		t.Fatalf("repeated scrap should not create extra history: before=%#v after=%#v", scrapHistory, repeatedScrapHistory)
	}
	repeatedScrapRecords := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(serverID)+"/retirement-records", token, nil)
	if len(repeatedScrapRecords["_array"].([]any)) != len(scrapRows) {
		t.Fatalf("repeated scrap should not create extra terminal record: before=%#v after=%#v", scrapRecords, repeatedScrapRecords)
	}
	scrappedServer := requestJSON(t, r, http.MethodPost, "/api/v1/servers", token, map[string]any{"asset_no": "BM-SCRAP-001", "hostname": "scrapped-node", "status": "scrapped", "tenant_id": "default"})
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(intFromJSON(t, scrappedServer, "id"))+"/retire", token, map[string]any{}, http.StatusConflict, "server.retire")
}

func TestPXEReadinessProbesRuntimeListeners(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ipxe.efi"), []byte("uefi loader"), 0o644); err != nil {
		t.Fatalf("write uefi bootfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "undionly.kpxe"), []byte("bios loader"), 0o644); err != nil {
		t.Fatalf("write bios bootfile: %v", err)
	}
	cfg := config.Config{
		AppEnv:                "test",
		BootServicesEnabled:   true,
		BootServiceMode:       "proxy",
		BootBindInterface:     "test-lab",
		BootDHCPListenAddr:    freeUDPAddr(t),
		BootDHCPServerIP:      "192.168.100.10",
		BootTFTPListenAddr:    freeUDPAddr(t),
		BootTFTPRoot:          root,
		BootTFTPBootfileUEFI:  "ipxe.efi",
		BootTFTPBootfileBIOS:  "undionly.kpxe",
		BootBaseURL:           "http://boot.test",
		ImageStorageDir:       t.TempDir(),
		ImageUploadMaxBytes:   1024 * 1024,
		ImageUploadMaxMBValid: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := services.NewPXEService(nil, cfg, t.Logf).Start(ctx); err != nil {
		t.Fatalf("start PXE runtime: %v", err)
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	status, message := pxeReadinessStatus(probeCtx, cfg, config.BootRuntimeIssues(cfg))
	if status != "ok" || !strings.Contains(message, "TFTP OACK blksize=1024") || !strings.Contains(message, "DHCP bootfile=ipxe.efi") {
		t.Fatalf("expected readiness to probe DHCP/TFTP listeners, got status=%q message=%q", status, message)
	}
}

func TestDeploymentPreflightRequiresBMCConnectivity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bmc unavailable", http.StatusInternalServerError)
	}))
	defer redfish.Close()

	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:bmc-preflight?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-BMC-PREFLIGHT", Hostname: "bmc-preflight", Status: "ready", Architecture: "x86_64"}
	image := models.Image{Name: "BMC preflight image", OSFamily: "ubuntu", OSVersion: "24.04", Architecture: "x86_64", FilePath: tempImageFile(t, cfg.ImageStorageDir), Status: "enabled", TestStatus: "tested_passed"}
	network := models.NetworkConfig{Name: "BMC preflight network", Purpose: "deployment", CIDR: "192.168.250.0/24", Gateway: "192.168.250.1", DHCPMode: "proxy", Status: "enabled"}
	installTemplate := models.InstallTemplate{Name: "BMC preflight install", TemplateType: "cloud-init", Content: "#cloud-config\n", Status: "enabled"}
	workflowTemplate := models.WorkflowTemplate{Name: "BMC preflight workflow", Version: "v1", Status: "enabled", Definition: datatypes.JSON([]byte(`{"steps":[{"name":"install","action":"install_os"}]}`))}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := db.Create(&network).Error; err != nil {
		t.Fatalf("create network: %v", err)
	}
	if err := db.Create(&installTemplate).Error; err != nil {
		t.Fatalf("create install template: %v", err)
	}
	if err := db.Create(&workflowTemplate).Error; err != nil {
		t.Fatalf("create workflow template: %v", err)
	}

	bmcResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc", token, map[string]any{"type": "redfish", "protocol": "http", "endpoint": redfish.URL, "username": "admin", "password": "Secret@123"}, "bmc.upsert")
	if bmcResp["status"] != "unknown" {
		t.Fatalf("new BMC endpoint should be unknown before connectivity check: %#v", bmcResp)
	}
	checkResp := requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc/check", token, map[string]any{}, http.StatusBadGateway, "")
	if checkResp["error"] != "bmc connectivity check failed" || checkResp["status"] != "error" || checkResp["checked_at"] == nil || !contains(checkResp["detail"].(string), "returned 500") {
		t.Fatalf("expected failed BMC check to expose connectivity status: %#v", checkResp)
	}
	proof, ok := checkResp["proof"].(map[string]any)
	if !ok || proof["adapter"] != "redfish" || proof["endpoint_status"] != "error" || proof["stage"] != "connectivity" {
		t.Fatalf("expected failed BMC check to include connectivity proof context: %#v", checkResp)
	}
	var failedCheckAudits int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "bmc.check", "server", itoa(int(server.ID))).Count(&failedCheckAudits).Error; err != nil {
		t.Fatalf("count failed bmc check audits: %v", err)
	}
	if failedCheckAudits != 1 {
		t.Fatalf("expected failed BMC check to be audited, got %d", failedCheckAudits)
	}

	deploymentResp := requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": server.ID, "image_id": image.ID, "template_id": installTemplate.ID, "workflow_id": workflowTemplate.ID}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(deploymentResp["problems"].([]any), "bmc connectivity check failed: redfish GET /redfish/v1 returned 500: bmc unavailable") {
		t.Fatalf("expected deployment preflight to require reachable BMC: %#v", deploymentResp)
	}
	var deployments int64
	if err := db.Model(&models.Deployment{}).Where("server_id = ?", server.ID).Count(&deployments).Error; err != nil {
		t.Fatalf("count deployments: %v", err)
	}
	if deployments != 0 {
		t.Fatalf("deployment should not be created when BMC preflight fails, got %d", deployments)
	}
	var endpoint models.BmcEndpoint
	if err := db.Where("server_id = ?", server.ID).First(&endpoint).Error; err != nil {
		t.Fatalf("load bmc endpoint: %v", err)
	}
	if endpoint.Status != "error" || endpoint.LastCheckedAt == nil {
		t.Fatalf("expected failed BMC preflight to update endpoint status: %#v", endpoint)
	}
}

func TestBMCFailureProofClassifiesConfigAndConnectivity(t *testing.T) {
	now := time.Now().UTC()
	endpoint := models.BmcEndpoint{Status: "error", LastCheckedAt: &now}
	configProof := bmcFailureProof("redfish", endpoint, errors.New("BMC endpoint type \"ipmi\" does not match BMC_ADAPTER=redfish"))
	if configProof.Adapter != "redfish" || configProof.EndpointStatus != "error" || configProof.Stage != "config" || configProof.LastCheckedAt == nil {
		t.Fatalf("expected config BMC failure proof, got %#v", configProof)
	}
	connectivityProof := bmcFailureProof("redfish", endpoint, errors.New("redfish GET /redfish/v1 returned 500"))
	if connectivityProof.Stage != "connectivity" {
		t.Fatalf("expected connectivity BMC failure proof, got %#v", connectivityProof)
	}
}

func TestBMCCheckWarnsWhenIdentityProofIsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1":
			_, _ = w.Write([]byte(`{"Systems":{"@odata.id":"/redfish/v1/Systems"}}`))
		case "/redfish/v1/Systems":
			_, _ = w.Write([]byte(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}`))
		case "/redfish/v1/Systems/1":
			_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/Systems/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer redfish.Close()

	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:bmc-empty-identity?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-BMC-EMPTY-IDENTITY", Hostname: "bmc-empty-identity", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc", token, map[string]any{"type": "redfish", "protocol": "http", "endpoint": redfish.URL, "username": "admin", "password": "Secret@123"}, "bmc.upsert")
	checkResp := requestJSON(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc/check", token, map[string]any{})
	proof, ok := checkResp["proof"].(map[string]any)
	if checkResp["status"] != "ok" || !ok || proof["adapter"] != "redfish" || !strings.Contains(checkResp["proof_error"].(string), "returned no manufacturer") {
		t.Fatalf("expected BMC check to warn when identity proof is empty: %#v", checkResp)
	}
}

func TestProductionRejectsPlainHTTPRedfishEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "production", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:bmc-production-redfish-https?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", RedfishInsecureTLS: false, CollectorMode: "simulated", BootBaseURL: "https://boot.example.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-BMC-PROD-HTTPS", Hostname: "bmc-prod-https", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}

	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc", token, map[string]any{"type": "redfish", "protocol": "http", "endpoint": "http://bmc.prod.test", "username": "admin", "password": "Secret@123"}, http.StatusBadRequest, "bmc.upsert")

	bmcResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/bmc", token, map[string]any{"type": "redfish", "protocol": "https", "endpoint": "https://bmc.prod.test", "username": "admin", "password": "Secret@123"}, "bmc.upsert")
	if bmcResp["protocol"] != "https" || bmcResp["endpoint"] != "https://bmc.prod.test" {
		t.Fatalf("expected production to accept HTTPS Redfish endpoint: %#v", bmcResp)
	}
}

func TestDeploymentRetryRestartsFailedWorkflow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:deployment-retry?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-RETRY-001", Hostname: "retry-node", Status: "ready", Architecture: "x86_64"}
	image := models.Image{Name: "Retry image", OSFamily: "ubuntu", OSVersion: "24.04", Architecture: "x86_64", FilePath: tempImageFile(t, cfg.ImageStorageDir), Status: "enabled", TestStatus: "tested_passed"}
	network := models.NetworkConfig{Name: "Retry deployment network", Purpose: "deployment", CIDR: "192.168.251.0/24", Gateway: "192.168.251.1", DHCPMode: "proxy", Status: "enabled"}
	installTemplate := models.InstallTemplate{Name: "Retry install", TemplateType: "cloud-init", Content: "#cloud-config\n", Status: "enabled"}
	workflowTemplate := models.WorkflowTemplate{Name: "Retry workflow", Version: "v1", Status: "enabled", Definition: datatypes.JSON([]byte(`{"steps":[{"name":"intentional failure","action":"simulate_failure"}]}`))}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := db.Create(&image).Error; err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := db.Create(&network).Error; err != nil {
		t.Fatalf("create network: %v", err)
	}
	if err := db.Create(&installTemplate).Error; err != nil {
		t.Fatalf("create install template: %v", err)
	}
	if err := db.Create(&workflowTemplate).Error; err != nil {
		t.Fatalf("create workflow template: %v", err)
	}
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Endpoint: "https://bmc.retry.test", Username: "admin", Protocol: "https", Status: "ok", PowerState: "on"}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}

	deployment := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": server.ID, "image_id": image.ID, "template_id": installTemplate.ID, "workflow_id": workflowTemplate.ID}), "deployment.create")
	deploymentID := intFromJSON(t, deployment, "id")
	time.Sleep(800 * time.Millisecond)
	failedDeployment := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentID), token, nil)
	if failedDeployment["status"] != "failed" || failedDeployment["error_message"] == "" {
		t.Fatalf("expected simulated workflow failure: %#v", failedDeployment)
	}
	var failedServer models.Server
	if err := db.First(&failedServer, server.ID).Error; err != nil {
		t.Fatalf("load failed server: %v", err)
	}
	if failedServer.Status != "ready" {
		t.Fatalf("failed deployment should restore server to ready, got %#v", failedServer)
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments/"+itoa(deploymentID)+"/retry", token, nil, http.StatusPreconditionRequired, "")

	workflowTemplate.Definition = datatypes.JSON([]byte(`{"steps":[{"name":"install","action":"install_os"}]}`))
	if err := db.Model(&workflowTemplate).Update("definition", workflowTemplate.Definition).Error; err != nil {
		t.Fatalf("update workflow template: %v", err)
	}
	retryResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments/"+itoa(deploymentID)+"/retry", token, nil, "deployment.retry")
	if retryResp["status"] != "pending" {
		t.Fatalf("expected retry to requeue deployment: %#v", retryResp)
	}
	time.Sleep(800 * time.Millisecond)
	finalDeployment := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentID), token, nil)
	if finalDeployment["status"] != "success" {
		t.Fatalf("expected retry to finish successfully: %#v", finalDeployment)
	}
	logs := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentID)+"/logs", token, nil)
	tasks := logs["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["status"] != "success" {
		t.Fatalf("deployment logs should show latest retry run: %#v", logs)
	}
	logSummary := logs["summary"].(map[string]any)
	if intFromJSON(t, logSummary, "total_runs") != 2 || intFromJSON(t, logSummary, "task_success") != 1 {
		t.Fatalf("deployment log summary should describe latest retry run: %#v", logs)
	}
	logRuns := logs["runs"].([]any)
	if len(logRuns) != 2 {
		t.Fatalf("deployment logs should include failed run history plus retry run: %#v", logs)
	}
	firstRun := logRuns[0].(map[string]any)
	secondRun := logRuns[1].(map[string]any)
	if firstRun["status"] != "failed" || secondRun["status"] != "success" || int(secondRun["attempt"].(float64)) != 2 {
		t.Fatalf("deployment run history should preserve attempts and statuses: %#v", logs)
	}
	var runs int64
	if err := db.Model(&models.WorkflowRun{}).Where("deployment_id = ?", deploymentID).Count(&runs).Error; err != nil {
		t.Fatalf("count workflow runs: %v", err)
	}
	if runs != 2 {
		t.Fatalf("expected failed run plus retry run, got %d", runs)
	}
	var retryAudits int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "deployment.retry", "deployment", itoa(deploymentID)).Count(&retryAudits).Error; err != nil {
		t.Fatalf("count retry audit logs: %v", err)
	}
	if retryAudits != 1 {
		t.Fatalf("expected one deployment.retry audit, got %d", retryAudits)
	}
}

func TestDeploymentConcurrencyQueuesPendingWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:deployment-concurrency?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false, DeploymentConcurrency: 1,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	serverA := models.Server{AssetNo: "BM-QUEUE-001", Hostname: "queue-node-1", Status: "ready", Architecture: "x86_64"}
	serverB := models.Server{AssetNo: "BM-QUEUE-002", Hostname: "queue-node-2", Status: "ready", Architecture: "x86_64"}
	image := models.Image{Name: "Queue image", OSFamily: "ubuntu", OSVersion: "24.04", Architecture: "x86_64", FilePath: tempImageFile(t, cfg.ImageStorageDir), Status: "enabled", TestStatus: "tested_passed"}
	network := models.NetworkConfig{Name: "Queue deployment network", Purpose: "deployment", CIDR: "192.168.252.0/24", Gateway: "192.168.252.1", DHCPMode: "proxy", Status: "enabled"}
	workflowTemplate := models.WorkflowTemplate{Name: "Queue workflow", Version: "v1", Status: "enabled", Definition: datatypes.JSON([]byte(queueWorkflowDefinition(12)))}
	for _, row := range []any{&serverA, &serverB, &image, &network, &workflowTemplate} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create queue fixture: %v", err)
		}
	}
	for _, server := range []models.Server{serverA, serverB} {
		bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Endpoint: fmt.Sprintf("https://bmc.queue.%d.test", server.ID), Username: "admin", Protocol: "https", Status: "ok", PowerState: "on"}
		if err := db.Create(&bmc).Error; err != nil {
			t.Fatalf("create bmc endpoint: %v", err)
		}
	}

	batchPayload := deploymentPayload(map[string]any{"server_ids": []uint{serverA.ID, serverB.ID}, "image_id": image.ID, "workflow_id": workflowTemplate.ID})
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments/batch", token, batchPayload, http.StatusPreconditionRequired, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/deployments/batch", token, deploymentPayload(map[string]any{"server_ids": []uint{serverA.ID, serverA.ID}, "image_id": image.ID, "workflow_id": workflowTemplate.ID}), http.StatusBadRequest, "deployment.batch-create")
	batchResp := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/deployments/batch", token, batchPayload, "deployment.batch-create")
	if intFromJSON(t, batchResp, "created") != 2 {
		t.Fatalf("expected two batch deployments: %#v", batchResp)
	}
	deployments := batchResp["deployments"].([]any)
	if len(deployments) != 2 {
		t.Fatalf("expected two deployment rows: %#v", batchResp)
	}
	deploymentAID := int(deployments[0].(map[string]any)["id"].(float64))
	deploymentBID := int(deployments[1].(map[string]any)["id"].(float64))

	waitForOneRunningOnePending(t, r, token, deploymentAID, deploymentBID, 10*time.Second)
	waitForDeploymentStatus(t, r, token, deploymentAID, 20*time.Second, "success")
	waitForDeploymentStatus(t, r, token, deploymentBID, 20*time.Second, "success")
}

func TestLabValidationRunOptionsAcceptLegacyPXEArch(t *testing.T) {
	opts, err := normalizeLabValidationRunOptions(labValidationRunOptions{})
	if err != nil {
		t.Fatalf("default PXE arch should be accepted: %v", err)
	}
	if opts.pxeArch != 9 {
		t.Fatalf("empty pxe_arch should default to UEFI x86_64 arch 9, got %d", opts.pxeArch)
	}
	if opts.SSHProbeCommand != services.DefaultSSHCheckCommand {
		t.Fatalf("empty ssh_probe_command should default to safe probe, got %q", opts.SSHProbeCommand)
	}
	opts, err = normalizeLabValidationRunOptions(labValidationRunOptions{SSHProbeCommand: "printf lab-proof"})
	if err != nil || opts.SSHProbeCommand != "printf lab-proof" {
		t.Fatalf("single-line ssh_probe_command should be accepted, opts=%#v err=%v", opts, err)
	}
	if _, err := normalizeLabValidationRunOptions(labValidationRunOptions{SSHProbeCommand: "hostname\nid"}); err == nil {
		t.Fatalf("multi-line ssh_probe_command should be rejected")
	}
	legacyArch := uint16(0)
	opts, err = normalizeLabValidationRunOptions(labValidationRunOptions{PXEArch: &legacyArch})
	if err != nil || opts.pxeArch != 0 {
		t.Fatalf("legacy BIOS pxe_arch should be accepted, opts=%#v err=%v", opts, err)
	}
	uefiArch := uint16(7)
	opts, err = normalizeLabValidationRunOptions(labValidationRunOptions{PXEArch: &uefiArch})
	if err != nil || opts.pxeArch != 7 {
		t.Fatalf("UEFI pxe_arch should be accepted, opts=%#v err=%v", opts, err)
	}
	badArch := uint16(12)
	if _, err := normalizeLabValidationRunOptions(labValidationRunOptions{PXEArch: &badArch}); err == nil {
		t.Fatalf("unsupported pxe_arch should be rejected")
	}
}

func TestSameLabSubjectNormalizesMACFormats(t *testing.T) {
	if !sameLabSubject("52-54-00-DD-00-01", "52:54:00:dd:00:01") {
		t.Fatalf("expected MAC subjects with different delimiters/case to match")
	}
	if sameLabSubject("52:54:00:dd:00:01", "52:54:00:dd:00:02") {
		t.Fatalf("different MAC subjects should not match")
	}
	if !sameLabSubject("PXE-LAB-RUN-1", "pxe-lab-run-1") {
		t.Fatalf("non-MAC subjects should remain case-insensitive")
	}
}

func TestStrictPXEBootEventRejectsAPIOnlySource(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:pxe-api-source?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	for _, event := range []models.BootEvent{
		{MAC: "52:54:00:aa:00:02", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.2", Source: ""},
		{MAC: "52:54:00:aa:00:03", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.3", Source: "unknown"},
	} {
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("create non-physical boot event: %v", err)
		}
		result := h.runLabPXEBootEventCheck(event.MAC)
		if result.Status != "failed" || !strings.Contains(result.Message, "requires http_ipxe or pxe_dhcp") {
			t.Fatalf("expected strict PXE check to reject non-physical source %q: %#v", event.Source, result)
		}
	}
	apiEvent := models.BootEvent{MAC: "52:54:00:aa:00:01", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "127.0.0.1", Source: "api_event"}
	if err := db.Create(&apiEvent).Error; err != nil {
		t.Fatalf("create api boot event: %v", err)
	}
	result := h.runLabPXEBootEventCheck("52:54:00:aa:00:01")
	if result.Status != "failed" || !strings.Contains(result.Message, "api_event") {
		t.Fatalf("expected strict PXE check to reject api_event source: %#v", result)
	}
	httpEvent := models.BootEvent{MAC: "52:54:00:aa:00:01", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.10", Source: "http_ipxe"}
	if err := db.Create(&httpEvent).Error; err != nil {
		t.Fatalf("create http boot event: %v", err)
	}
	result = h.runLabPXEBootEventCheck("52:54:00:aa:00:01")
	if result.Status != "success" || !strings.Contains(result.Message, "http_ipxe") {
		t.Fatalf("expected strict PXE check to accept http_ipxe source: %#v", result)
	}
	laterAPIEvent := models.BootEvent{MAC: "52:54:00:aa:00:01", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "127.0.0.1", Source: "api_event"}
	if err := db.Create(&laterAPIEvent).Error; err != nil {
		t.Fatalf("create later api boot event: %v", err)
	}
	result = h.runLabPXEBootEventCheck("52:54:00:aa:00:01")
	if result.Status != "success" || !strings.Contains(result.Message, "http_ipxe") {
		t.Fatalf("expected strict PXE check to prefer fresh physical event over later api_event: %#v", result)
	}

	stalePhysicalEvent := models.BootEvent{MAC: "52:54:00:aa:00:04", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.44", Source: "http_ipxe", CreatedAt: time.Now().UTC().Add(-8 * 24 * time.Hour)}
	if err := db.Create(&stalePhysicalEvent).Error; err != nil {
		t.Fatalf("create stale physical boot event: %v", err)
	}
	result = h.runLabPXEBootEventCheck("52:54:00:aa:00:04")
	if result.Status != "failed" || !strings.Contains(result.Message, "fresh event") {
		t.Fatalf("expected strict PXE check to reject stale physical event: %#v", result)
	}
}

func TestStrictPXEBootEventRequiresRequestedServerMatch(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:pxe-requested-server-match?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-PXE-TARGET-001", Hostname: "pxe-target", PrimaryMAC: "52:54:00:aa:00:08", Status: "ready"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}

	otherEvent := models.BootEvent{MAC: "52:54:00:aa:00:09", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.90", Source: "http_ipxe"}
	if err := db.Create(&otherEvent).Error; err != nil {
		t.Fatalf("create other boot event: %v", err)
	}
	result := h.runLabPXEBootEventCheck(otherEvent.MAC, server.ID)
	if result.Status != "failed" || !strings.Contains(result.Message, "does not match requested server_ids") {
		t.Fatalf("expected strict PXE check to reject MAC outside requested server targets: %#v", result)
	}

	targetEvent := models.BootEvent{MAC: server.PrimaryMAC, Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.91", Source: "http_ipxe"}
	if err := db.Create(&targetEvent).Error; err != nil {
		t.Fatalf("create target boot event: %v", err)
	}
	result = h.runLabPXEBootEventCheck(server.PrimaryMAC, server.ID)
	if result.Status != "success" || result.ServerID != server.ID || result.AssetNo != server.AssetNo {
		t.Fatalf("expected unbound PXE boot event to match requested server primary MAC: %#v", result)
	}

	otherServer := models.Server{AssetNo: "BM-PXE-TARGET-002", Hostname: "pxe-other", PrimaryMAC: "52:54:00:aa:00:10", Status: "ready"}
	if err := db.Create(&otherServer).Error; err != nil {
		t.Fatalf("create other server: %v", err)
	}
	explicitOtherEvent := models.BootEvent{MAC: server.PrimaryMAC, Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.92", Source: "http_ipxe", ServerID: &otherServer.ID}
	if err := db.Create(&explicitOtherEvent).Error; err != nil {
		t.Fatalf("create explicit other boot event: %v", err)
	}
	result = h.runLabPXEBootEventCheck(server.PrimaryMAC, server.ID)
	if result.Status != "failed" || !strings.Contains(result.Message, "does not match requested server_ids") {
		t.Fatalf("expected explicit mismatched server_id to reject PXE proof: %#v", result)
	}
}

func TestLabValidationTargetsPreferPhysicalPXEEventOverLaterAPIEvent(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:pxe-target-physical-preference?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-PXE-PREF-001", Hostname: "pxe-preference", PrimaryMAC: "52:54:00:aa:00:05", Status: "ready"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	now := time.Now().UTC()
	physical := models.BootEvent{MAC: server.PrimaryMAC, Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.55", Source: "http_ipxe", ServerID: &server.ID, CreatedAt: now.Add(-time.Minute)}
	if err := db.Create(&physical).Error; err != nil {
		t.Fatalf("create physical boot event: %v", err)
	}
	apiOnly := models.BootEvent{MAC: server.PrimaryMAC, Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "127.0.0.1", Source: "api_event", ServerID: &server.ID, CreatedAt: now}
	if err := db.Create(&apiOnly).Error; err != nil {
		t.Fatalf("create api boot event: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 {
		t.Fatalf("expected one incomplete target, ready=%d targets=%#v", readyCount, targets)
	}
	if targets[0].PXEStatus != "ok" || targets[0].PXEBootEventID == nil || *targets[0].PXEBootEventID != physical.ID {
		t.Fatalf("expected target to prefer physical PXE event over later api_event: %#v", targets[0])
	}
}

func TestHTTPIPXESourceRequiresDeploymentNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:http-ipxe-source-network?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)

	_ = requestTextWithRemote(t, r, http.MethodGet, "/boot/ipxe?mac=52:54:00:aa:10:01&arch=x86_64&firmware=uefi", "", nil, "203.0.113.10:1234")
	var outsideEvent models.BootEvent
	if err := db.Where("mac = ?", "52:54:00:aa:10:01").Order("id desc").First(&outsideEvent).Error; err != nil {
		t.Fatalf("load outside HTTP iPXE boot event: %v", err)
	}
	if outsideEvent.Source != "api_event" {
		t.Fatalf("expected non-deployment HTTP iPXE request to be non-physical api_event, got %#v", outsideEvent)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	if result := h.runLabPXEBootEventCheck("52:54:00:aa:10:01"); result.Status != "failed" || !strings.Contains(result.Message, "api_event") {
		t.Fatalf("expected strict PXE check to reject non-deployment HTTP iPXE source: %#v", result)
	}

	network := models.NetworkConfig{Name: "Lab deployment VLAN", Purpose: "deployment", CIDR: "192.0.2.0/24", Status: "enabled", DHCPMode: "proxy", ProxyDHCP: true}
	if err := db.Create(&network).Error; err != nil {
		t.Fatalf("create deployment network: %v", err)
	}
	_ = requestTextWithRemote(t, r, http.MethodGet, "/boot/ipxe?mac=52:54:00:aa:10:02&arch=x86_64&firmware=uefi", "", nil, "192.0.2.55:1234")
	var insideEvent models.BootEvent
	if err := db.Where("mac = ?", "52:54:00:aa:10:02").Order("id desc").First(&insideEvent).Error; err != nil {
		t.Fatalf("load inside HTTP iPXE boot event: %v", err)
	}
	if insideEvent.Source != "http_ipxe" {
		t.Fatalf("expected deployment-network HTTP iPXE request to be physical http_ipxe, got %#v", insideEvent)
	}
	if result := h.runLabPXEBootEventCheck("52:54:00:aa:10:02"); result.Status != "success" || !strings.Contains(result.Message, "http_ipxe") {
		t.Fatalf("expected strict PXE check to accept deployment-network HTTP iPXE source: %#v", result)
	}

	req := httptest.NewRequest(http.MethodGet, "/boot/ipxe?mac=52:54:00:aa:10:03&arch=x86_64&firmware=uefi", nil)
	req.RemoteAddr = "192.0.2.56:1234"
	req.Header.Set(labValidationHTTPProbeHeader, "1")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected lab HTTP probe iPXE request to succeed, got %d: %s", res.Code, res.Body.String())
	}
	var probeEvent models.BootEvent
	if err := db.Where("mac = ?", "52:54:00:aa:10:03").Order("id desc").First(&probeEvent).Error; err != nil {
		t.Fatalf("load lab HTTP probe boot event: %v", err)
	}
	if probeEvent.Source != "api_event" {
		t.Fatalf("expected lab HTTP probe request to be non-physical api_event, got %#v", probeEvent)
	}
	if result := h.runLabPXEBootEventCheck("52:54:00:aa:10:03"); result.Status != "failed" || !strings.Contains(result.Message, "api_event") {
		t.Fatalf("expected strict PXE check to reject lab HTTP probe source: %#v", result)
	}
}

func TestLabEvidenceRejectsAPIOnlyBootEventSource(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-evidence-api-source?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-PXE-EVIDENCE", Hostname: "pxe-evidence", Status: "ready", Architecture: "x86_64", PrimaryMAC: "52:54:00:aa:00:02"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	apiEvent := models.BootEvent{MAC: "52:54:00:aa:00:02", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "127.0.0.1", Source: "api_event"}
	if err := db.Create(&apiEvent).Error; err != nil {
		t.Fatalf("create api boot event: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}

	pxeEvidence := models.LabValidationEvidence{Kind: "pxe", Subject: "52:54:00:aa:00:02", Status: "ok", Summary: "pxe evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&pxeEvidence, nil, nil, &apiEvent.ID); err == nil || !strings.Contains(err.Error(), "api_event") {
		t.Fatalf("expected ok PXE evidence to reject api_event boot event, err=%v evidence=%#v", err, pxeEvidence)
	}
	fullEvidence := models.LabValidationEvidence{Kind: "full", Subject: "full-chain", Status: "ok", Summary: "full evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&fullEvidence, nil, &server.ID, &apiEvent.ID); err == nil || !strings.Contains(err.Error(), "api_event") {
		t.Fatalf("expected ok full evidence to reject api_event boot event, err=%v evidence=%#v", err, fullEvidence)
	}
	now := time.Now().UTC()
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.pxe-evidence.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.61", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	storedBadEvidence := models.LabValidationEvidence{
		Kind: "full", Subject: "full-chain", Status: "ok", Summary: "historical full evidence with api-only boot event", CreatedBy: "tester@example.com",
		ServerID: &server.ID, BootEventID: &apiEvent.ID, BmcEndpointID: &bmc.ID, SSHAccessID: &ssh.ID,
	}
	if err := db.Create(&storedBadEvidence).Error; err != nil {
		t.Fatalf("create historical bad evidence: %v", err)
	}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 || targets[0].EvidenceStatus == "ok" || targets[0].FullChainReady {
		t.Fatalf("historical api_event evidence must not make a target ready: ready=%d targets=%#v", readyCount, targets)
	}

	httpEvent := models.BootEvent{MAC: "52:54:00:aa:00:02", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.60", Source: "http_ipxe"}
	if err := db.Create(&httpEvent).Error; err != nil {
		t.Fatalf("create http boot event: %v", err)
	}
	pxeEvidence = models.LabValidationEvidence{Kind: "pxe", Subject: "52:54:00:aa:00:02", Status: "ok", Summary: "pxe evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&pxeEvidence, nil, &server.ID, &httpEvent.ID); err != nil {
		t.Fatalf("expected ok PXE evidence to accept http_ipxe boot event: %v", err)
	}
}

func TestLabEvidenceRejectsBMCAdapterMismatch(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-bmc-adapter-mismatch?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-BMC-MISMATCH", Hostname: "bmc-mismatch", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: server.ID, Type: "ipmi", Protocol: "ipmi", Endpoint: "10.0.0.5", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	evidence := models.LabValidationEvidence{Kind: "bmc", Subject: "bmc-mismatch", Status: "ok", Summary: "bmc evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&evidence, nil, &server.ID, nil); err == nil || !strings.Contains(err.Error(), "current BMC_ADAPTER") {
		t.Fatalf("expected BMC evidence to reject adapter mismatch, err=%v evidence=%#v", err, evidence)
	}
	storedMismatchEvidence := models.LabValidationEvidence{Kind: "bmc", Subject: "bmc-mismatch", Status: "ok", Summary: "historical bmc evidence", CreatedBy: "tester@example.com", ServerID: &server.ID, BmcEndpointID: &endpoint.ID}
	if err := db.Create(&storedMismatchEvidence).Error; err != nil {
		t.Fatalf("create historical bmc mismatch evidence: %v", err)
	}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 || targets[0].BMCStatus != "adapter_mismatch" || targets[0].EvidenceStatus == "ok" || !strings.Contains(strings.Join(targets[0].BlockingReasons, "; "), "BMC endpoint type") {
		t.Fatalf("expected target matrix to flag BMC adapter mismatch: ready=%d targets=%#v", readyCount, targets)
	}
}

func TestProductionLabValidationRejectsPlainHTTPRedfishEndpoint(t *testing.T) {
	cfg := config.Config{
		AppEnv: "production", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-production-redfish-https?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", RedfishInsecureTLS: false, CollectorMode: "simulated", BootBaseURL: "https://boot.example.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-BMC-PROD-POLICY", Hostname: "bmc-prod-policy", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "http", Endpoint: "http://bmc.prod-policy.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}

	evidence := models.LabValidationEvidence{Kind: "bmc", Subject: "bmc-prod-policy", Status: "ok", Summary: "bmc evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&evidence, nil, &server.ID, nil); err == nil || !strings.Contains(err.Error(), "production Redfish endpoint must use https") {
		t.Fatalf("expected production HTTP Redfish evidence to be rejected, err=%v evidence=%#v", err, evidence)
	}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 || targets[0].BMCStatus != "adapter_mismatch" || !strings.Contains(strings.Join(targets[0].BlockingReasons, "; "), "production Redfish endpoint must use https") {
		t.Fatalf("expected target matrix to flag production HTTP Redfish endpoint: ready=%d targets=%#v", readyCount, targets)
	}
	report := labValidationReport{Status: "ok"}
	h.fillBMCLabValidation(&report)
	if !labCheckContains(report.Checks, "bmc_connectivity", "error", "Redfish security policy") {
		t.Fatalf("expected BMC connectivity check to flag Redfish security policy: %#v", report.Checks)
	}
	results := h.runLabBMCChecks(context.Background(), 10, []uint{server.ID}, true)
	if len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Message, "production Redfish endpoint must use https") {
		t.Fatalf("expected strict BMC run to reject production HTTP Redfish before probing: %#v", results)
	}
	var details map[string]any
	if err := json.Unmarshal(results[0].Details, &details); err != nil {
		t.Fatalf("expected strict BMC config failure details JSON: %v raw=%s", err, string(results[0].Details))
	}
	if details["adapter"] != "redfish" || details["stage"] != "config" || details["endpoint_status"] != "ok" {
		t.Fatalf("expected strict BMC config failure proof details: %#v", details)
	}
}

func TestProductionLabValidationRequiresKnownHostsForSSHProof(t *testing.T) {
	cfg := config.Config{
		AppEnv: "production", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-production-ssh-known-hosts?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "ssh", SSHOperationsMode: "ssh", SSHHostKeyPolicy: "insecure_ignore", BootBaseURL: "https://boot.example.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-SSH-PROD-POLICY", Hostname: "ssh-prod-policy", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.20", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}

	evidence := models.LabValidationEvidence{Kind: "ssh", Subject: "ssh-prod-policy", Status: "ok", Summary: "ssh evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&evidence, nil, &server.ID, nil); err == nil || !strings.Contains(err.Error(), "production SSH validation requires SSH_HOST_KEY_POLICY=known_hosts") {
		t.Fatalf("expected production insecure SSH evidence to be rejected, err=%v evidence=%#v", err, evidence)
	}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 || targets[0].SSHStatus != "policy_mismatch" || !strings.Contains(strings.Join(targets[0].BlockingReasons, "; "), "production SSH validation requires SSH_HOST_KEY_POLICY=known_hosts") {
		t.Fatalf("expected target matrix to flag SSH host key policy: ready=%d targets=%#v", readyCount, targets)
	}
	report := labValidationReport{Status: "ok"}
	h.fillSSHLabValidation(&report)
	if !labCheckContains(report.Checks, "ssh_modes", "error", "SSH_HOST_KEY_POLICY=known_hosts") {
		t.Fatalf("expected SSH mode check to flag host key policy: %#v", report.Checks)
	}
	results := h.runLabSSHChecks(context.Background(), 10, []uint{server.ID}, services.DefaultSSHCheckCommand)
	if len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Message, "production SSH validation requires SSH_HOST_KEY_POLICY=known_hosts") {
		t.Fatalf("expected strict SSH run to reject insecure host key policy before probing: %#v", results)
	}
}

func TestReadinessAndLabValidationReportKnownHostsCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, []byte("other.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:known-hosts-coverage?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "ssh", SSHOperationsMode: "ssh", SSHHostKeyPolicy: "known_hosts", SSHKnownHostsPath: knownHostsPath, SSHConnectTimeout: time.Second, BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-KNOWN-HOSTS", Hostname: "known-hosts-node", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := db.Create(&models.SSHAccess{ServerID: server.ID, Host: "missing.example.com", Port: 22, Username: "root", AuthType: "password", Status: "configured"}).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	readyz := requestJSON(t, r, http.MethodGet, "/readyz", "", nil)
	if readyz["status"] != "degraded" || !jsonCheckContains(readyz["checks"].([]any), "ssh_known_hosts", "error", "missing.example.com") {
		t.Fatalf("expected readyz to report missing known_hosts target: %#v", readyz)
	}
	report := requestJSON(t, r, http.MethodGet, "/api/v1/system/lab-validation", token, nil)
	if !jsonCheckContains(report["checks"].([]any), "ssh_known_hosts", "error", "missing.example.com") {
		t.Fatalf("expected lab validation to report missing known_hosts target: %#v", report)
	}
}

func TestReadinessAcceptsHashedKnownHostsCoverage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.HashHostname("hashed.example.com") + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
	if err := os.WriteFile(knownHostsPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:hashed-known-hosts-coverage?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "ssh", SSHOperationsMode: "ssh", SSHHostKeyPolicy: "known_hosts", SSHKnownHostsPath: knownHostsPath, SSHConnectTimeout: time.Second, BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-HASHED-KNOWN-HOSTS", Hostname: "hashed-known-hosts-node", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := db.Create(&models.SSHAccess{ServerID: server.ID, Host: "hashed.example.com", Port: 22, Username: "root", AuthType: "password", Status: "configured"}).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	r := NewRouter(db, cfg)

	readyz := requestJSON(t, r, http.MethodGet, "/readyz", "", nil)
	if !jsonCheckContains(readyz["checks"].([]any), "ssh_known_hosts", "ok", "hashed host pattern") {
		t.Fatalf("expected readyz to accept hashed known_hosts target coverage: %#v", readyz)
	}
}

func TestBMCToolingStatusForIPMI(t *testing.T) {
	original := lookExecutable
	t.Cleanup(func() { lookExecutable = original })

	lookExecutable = func(name string) (string, error) {
		if name != "ipmitool" {
			t.Fatalf("unexpected executable lookup %q", name)
		}
		return "", os.ErrNotExist
	}
	status, message := bmcToolingStatus("ipmi")
	if status != "error" || !strings.Contains(message, "ipmitool") {
		t.Fatalf("expected missing ipmitool error, got status=%q message=%q", status, message)
	}

	lookExecutable = func(name string) (string, error) {
		return "/usr/bin/ipmitool", nil
	}
	status, message = bmcToolingStatus("ipmi")
	if status != "ok" || !strings.Contains(message, "/usr/bin/ipmitool") {
		t.Fatalf("expected ipmitool ok, got status=%q message=%q", status, message)
	}

	status, message = bmcToolingStatus("redfish")
	if status != "ok" || !strings.Contains(message, "HTTP") {
		t.Fatalf("expected redfish built-in tooling ok, got status=%q message=%q", status, message)
	}
}

func TestStrictLabBMCCheckRecordsFirmwareIdentity(t *testing.T) {
	redfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "Secret@123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/redfish/v1":
			_ = json.NewEncoder(w).Encode(map[string]any{"RedfishVersion": "1.15.0"})
		case "/redfish/v1/Systems":
			_ = json.NewEncoder(w).Encode(map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/System.1"}}})
		case "/redfish/v1/Systems/System.1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"@odata.id":       "/redfish/v1/Systems/System.1",
				"PowerState":      "On",
				"Manufacturer":    "Dell Inc.",
				"Model":           "PowerEdge R650",
				"SerialNumber":    "LAB-BMC-001",
				"BiosVersion":     "2.10.1",
				"FirmwareVersion": "system-fw",
			})
		case "/redfish/v1/Managers":
			_ = json.NewEncoder(w).Encode(map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Managers/iDRAC.1"}}})
		case "/redfish/v1/Managers/iDRAC.1":
			_ = json.NewEncoder(w).Encode(map[string]any{"FirmwareVersion": "7.10.30.00", "Model": "iDRAC9"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer redfish.Close()

	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:strict-bmc-identity?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "strict-bmc-identity-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-BMC-IDENTITY", Hostname: "bmc-identity", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	credential, err := services.NewCredentialService(db, cfg).Store("strict-bmc-password", "bmc", "admin", "Secret@123", "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "http", Endpoint: redfish.URL, Username: "admin", EncryptedPasswordRef: itoa(int(credential.ID)), Status: "unknown"}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	results := h.runLabBMCChecks(context.Background(), 10, []uint{server.ID}, true)
	if len(results) != 1 || results[0].Status != "success" || !strings.Contains(results[0].Message, "manufacturer=Dell Inc.") || !strings.Contains(results[0].Message, "serial=LAB-BMC-001") || !strings.Contains(results[0].Message, "bmc=7.10.30.00") {
		t.Fatalf("expected strict BMC identity proof in result: %#v", results)
	}
	var details map[string]any
	if err := json.Unmarshal(results[0].Details, &details); err != nil {
		t.Fatalf("expected BMC identity details JSON: %v details=%s", err, string(results[0].Details))
	}
	if details["manufacturer"] != "Dell Inc." || details["serial_number"] != "LAB-BMC-001" || details["bmc_version"] != "7.10.30.00" {
		t.Fatalf("expected structured BMC identity details: %#v", details)
	}
	run := models.LabValidationRun{Status: "running", Strict: true, CheckBMC: true, Limit: 10, ServerIDs: labJSONValue([]uint{server.ID}), RequestedBy: "tester@example.com", RequestID: "strict-bmc-details", StartedAt: time.Now().UTC()}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create lab validation run: %v", err)
	}
	if err := h.persistLabValidationRunResults(run.ID, results); err != nil {
		t.Fatalf("persist lab validation run result: %v", err)
	}
	var stored models.LabValidationRunResult
	if err := db.Where("run_id = ? AND kind = ?", run.ID, "bmc").First(&stored).Error; err != nil {
		t.Fatalf("load stored BMC run result: %v", err)
	}
	if err := json.Unmarshal(stored.Details, &details); err != nil || details["model"] != "PowerEdge R650" {
		t.Fatalf("expected persisted BMC identity details, err=%v details=%#v raw=%s", err, details, string(stored.Details))
	}
}

func TestStrictLabBMCCheckRecordsConnectivityFailureProof(t *testing.T) {
	redfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bmc unavailable", http.StatusInternalServerError)
	}))
	defer redfish.Close()

	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:strict-bmc-connectivity-failure?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "strict-bmc-connectivity-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	server := models.Server{AssetNo: "BM-BMC-CONN-FAIL", Hostname: "bmc-connectivity-failure", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	credential, err := services.NewCredentialService(db, cfg).Store("strict-bmc-connectivity-password", "bmc", "admin", "Secret@123", "tester@example.com")
	if err != nil {
		t.Fatalf("store credential: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "http", Endpoint: redfish.URL, Username: "admin", EncryptedPasswordRef: itoa(int(credential.ID)), Status: "unknown"}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	results := h.runLabBMCChecks(context.Background(), 10, []uint{server.ID}, true)
	if len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Message, "returned 500") {
		t.Fatalf("expected strict BMC connectivity failure result: %#v", results)
	}
	var details map[string]any
	if err := json.Unmarshal(results[0].Details, &details); err != nil {
		t.Fatalf("expected strict BMC connectivity failure details JSON: %v raw=%s", err, string(results[0].Details))
	}
	if details["adapter"] != "redfish" || details["stage"] != "connectivity" || details["endpoint_status"] != "error" || details["last_checked_at"] == nil {
		t.Fatalf("expected strict BMC connectivity failure proof details: %#v", details)
	}
}

func TestLabRemoteCommandDetailsIncludesSSHProof(t *testing.T) {
	detailsJSON := labRemoteCommandDetails("printf proof", services.RemoteCommandResult{
		ExitCode:         17,
		Stdout:           "proof-host\nLinux lab 6.1.0 x86_64",
		Stderr:           "permission warning",
		HostKeyPolicy:    "known_hosts",
		HostKeyVerified:  true,
		HostKeyAlgorithm: "ssh-ed25519",
		HostKeySHA256:    "SHA256:testfingerprint",
		HostKeyHost:      "[192.0.2.10]:22",
		HostKeyRemote:    "192.0.2.10:22",
	}, errors.New("remote command failed"))
	var details map[string]any
	if err := json.Unmarshal(detailsJSON, &details); err != nil {
		t.Fatalf("expected SSH details JSON: %v raw=%s", err, string(detailsJSON))
	}
	if details["command"] != "printf proof" || details["exit_code"] != float64(17) {
		t.Fatalf("expected command and exit code details: %#v", details)
	}
	if !strings.Contains(details["stdout"].(string), "proof-host") || !strings.Contains(details["stderr"].(string), "permission warning") || !strings.Contains(details["error"].(string), "remote command failed") {
		t.Fatalf("expected stdout/stderr/error details: %#v", details)
	}
	if details["host_key_policy"] != "known_hosts" || details["host_key_verified"] != true || details["host_key_algorithm"] != "ssh-ed25519" || details["host_key_sha256"] != "SHA256:testfingerprint" {
		t.Fatalf("expected SSH host key proof details: %#v", details)
	}
}

func TestLabSSHResultHasCommandProofRequiresKnownHostsAndStdout(t *testing.T) {
	result := labValidationRunResult{
		Status:  "success",
		Details: datatypes.JSON([]byte(`{"command":"true","exit_code":0}`)),
	}
	if labSSHResultHasCommandProof(result) {
		t.Fatalf("SSH proof without stdout must not count")
	}
	result.Details = datatypes.JSON([]byte(`{"command":"printf proof","exit_code":0,"stdout":"proof-host Linux x86_64"}`))
	if labSSHResultHasCommandProof(result) {
		t.Fatalf("SSH proof without known_hosts host key verification must not count")
	}
	result.Details = datatypes.JSON([]byte(`{"command":"printf proof","exit_code":0,"stdout":"proof-host Linux x86_64","host_key_policy":"insecure_ignore","host_key_verified":false,"host_key_algorithm":"ssh-ed25519","host_key_sha256":"SHA256:test"}`))
	if labSSHResultHasCommandProof(result) {
		t.Fatalf("SSH proof with insecure host key policy must not count")
	}
	result.Details = datatypes.JSON([]byte(`{"command":"printf proof","exit_code":0,"stdout":"proof-host Linux x86_64","host_key_policy":"known_hosts","host_key_verified":true,"host_key_algorithm":"ssh-ed25519","host_key_sha256":"SHA256:test"}`))
	if !labSSHResultHasCommandProof(result) {
		t.Fatalf("SSH proof with known_hosts verification, command, exit_code=0, and stdout should count")
	}
	result.Details = datatypes.JSON([]byte(`{"command":"printf proof","exit_code":0,"stdout":"proof-host","host_key_policy":"known_hosts","host_key_verified":true,"host_key_algorithm":"ssh-ed25519","host_key_sha256":"SHA256:test","error":"unexpected"}`))
	if labSSHResultHasCommandProof(result) {
		t.Fatalf("SSH proof with an error field must not count")
	}
}

func TestSSHCheckProofPayloadIncludesHostKeyProof(t *testing.T) {
	payload := sshCheckProofPayload("printf proof", services.RemoteCommandResult{
		ExitCode:         0,
		Stdout:           "proof-host Linux x86_64\n",
		HostKeyPolicy:    "known_hosts",
		HostKeyVerified:  true,
		HostKeyAlgorithm: "ssh-ed25519",
		HostKeySHA256:    "SHA256:testfingerprint",
		HostKeyHost:      "[192.0.2.10]:22",
		HostKeyRemote:    "192.0.2.10:22",
	})
	if payload["command"] != "printf proof" || payload["exit_code"] != 0 || payload["host_key_policy"] != "known_hosts" || payload["host_key_verified"] != true || payload["host_key_algorithm"] != "ssh-ed25519" || payload["host_key_sha256"] != "SHA256:testfingerprint" {
		t.Fatalf("expected SSH check proof fields, got %#v", payload)
	}
	if !strings.Contains(payload["stdout"].(string), "proof-host") {
		t.Fatalf("expected SSH check proof stdout, got %#v", payload)
	}
}

func TestSSHCheckProofPayloadIncludesFailureStage(t *testing.T) {
	payload := sshCheckProofPayload("printf proof", services.RemoteCommandResult{
		ExitCode:        -1,
		FailureStage:    "handshake",
		HostKeyPolicy:   "known_hosts",
		HostKeyVerified: false,
		HostKeySHA256:   "SHA256:mismatch",
	})
	if payload["stage"] != "handshake" || payload["host_key_policy"] != "known_hosts" || payload["host_key_verified"] != false || payload["host_key_sha256"] != "SHA256:mismatch" {
		t.Fatalf("expected SSH failure proof fields, got %#v", payload)
	}
}

func TestLabBMCResultHasIdentityProofRequiresPhysicalAdapter(t *testing.T) {
	result := labValidationRunResult{
		Status:  "success",
		Details: datatypes.JSON([]byte(`{"manufacturer":"Dell Inc.","model":"PowerEdge R650"}`)),
	}
	if labBMCResultHasIdentityProof(result) {
		t.Fatalf("BMC proof without redfish/ipmi adapter must not count")
	}
	result.Details = datatypes.JSON([]byte(`{"adapter":"simulated","manufacturer":"Dell Inc.","model":"PowerEdge R650"}`))
	if labBMCResultHasIdentityProof(result) {
		t.Fatalf("simulated BMC proof must not count")
	}
	result.Details = datatypes.JSON([]byte(`{"adapter":"redfish","manufacturer":"Dell Inc.","model":"PowerEdge R650"}`))
	if !labBMCResultHasIdentityProof(result) {
		t.Fatalf("redfish BMC proof with identity fields should count")
	}
	result.Details = datatypes.JSON([]byte(`{"adapter":"ipmi","manufacturer_id":"674","product_id":"256","device_id":"32"}`))
	if !labBMCResultHasIdentityProof(result) {
		t.Fatalf("ipmi BMC proof with hardware IDs should count")
	}
}

func TestLabOperatorChecklistRequiresRunProofDetails(t *testing.T) {
	h := Handler{}
	now := time.Now().UTC()
	serverID := uint(42)
	detail := labValidationRunDetail{
		Run: labValidationRunSummary{
			ID: 101, Strict: true, CheckPXE: true, CheckBMC: true, CheckSSH: true,
			ServerIDs: []uint{serverID}, PXEMACs: []string{"52:54:00:aa:bb:cc"},
		},
		Results: []labValidationRunResult{
			{RunID: 101, Kind: "bmc", ServerID: serverID, Status: "success", Message: "BMC connectivity passed", CheckedAt: &now},
			{RunID: 101, Kind: "ssh", ServerID: serverID, Status: "success", Message: "SSH probe passed", CheckedAt: &now},
		},
	}
	bootEventID := uint(7)
	target := labValidationTarget{
		ServerID: serverID, AssetNo: "BM-CHECKLIST-PROOF", PrimaryMAC: "52:54:00:aa:bb:cc",
		PXEStatus: "ok", PXEBootEventID: &bootEventID, BMCStatus: "ok", SSHStatus: "ok",
	}
	checklist := h.labOperatorChecklist(detail, []labValidationTarget{target}, []labBootEvent{{ID: bootEventID, MAC: "52:54:00:aa:bb:cc", Source: "http_ipxe"}})
	if labChecklistHas(checklist, serverID, "bmc_identity", "ok") || labChecklistHas(checklist, serverID, "ssh_command", "ok") {
		t.Fatalf("checklist must not mark BMC/SSH proof ok without structured run details: %#v", checklist)
	}
	if !labChecklistHas(checklist, serverID, "bmc_identity", "warning") || !labChecklistHas(checklist, serverID, "ssh_command", "warning") {
		t.Fatalf("expected BMC/SSH checklist warnings for missing proof details: %#v", checklist)
	}
}

func TestNetworkBootRuntimeStatusForPhysicalPXE(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ipxe.efi"), []byte("uefi"), 0o644); err != nil {
		t.Fatalf("write uefi bootfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "undionly.kpxe"), []byte("bios"), 0o644); err != nil {
		t.Fatalf("write bios bootfile: %v", err)
	}
	cfg := config.Config{
		BootServicesEnabled:  true,
		BootServiceMode:      "proxy",
		BootBindInterface:    "vlan100",
		BootDHCPListenAddr:   "192.168.100.10:67",
		BootDHCPServerIP:     "192.168.100.10",
		BootTFTPListenAddr:   "192.168.100.10:69",
		BootTFTPRoot:         root,
		BootTFTPBootfileUEFI: "ipxe.efi",
		BootTFTPBootfileBIOS: "undionly.kpxe",
	}
	network := models.NetworkConfig{Purpose: "deployment", Status: "enabled", CIDR: "192.168.100.0/24", DHCPMode: "proxy", ProxyDHCP: true}
	status, message := networkBootRuntimeStatus(cfg, network)
	if status != "ok" || !strings.Contains(message, "ProxyDHCP and TFTP runtime are enabled") {
		t.Fatalf("expected matching proxy runtime to pass, got status=%q message=%q", status, message)
	}

	cfg.BootDHCPServerIP = "192.168.101.10"
	status, message = networkBootRuntimeStatus(cfg, network)
	if status != "warning" || !strings.Contains(message, "BOOT_DHCP_SERVER_IP") {
		t.Fatalf("expected DHCP server IP outside deployment CIDR warning, got status=%q message=%q", status, message)
	}
	cfg.BootDHCPServerIP = "192.168.100.10"
	cfg.BootTFTPListenAddr = "192.168.101.10:69"
	status, message = networkBootRuntimeStatus(cfg, network)
	if status != "warning" || !strings.Contains(message, "BOOT_TFTP_LISTEN_ADDR") {
		t.Fatalf("expected TFTP listen IP outside deployment CIDR warning, got status=%q message=%q", status, message)
	}
	cfg.BootTFTPListenAddr = "192.168.100.10:69"

	cfg.BootServiceMode = "external"
	status, message = networkBootRuntimeStatus(cfg, network)
	if status != "warning" || !strings.Contains(message, "starts only TFTP") {
		t.Fatalf("expected mode mismatch warning, got status=%q message=%q", status, message)
	}
}

func TestLabEvidenceRequiresFreshOKEvidence(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-evidence-freshness?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	oldTime := time.Now().UTC().Add(-labEvidenceFreshnessWindow - time.Hour)
	for _, kind := range []string{"pxe", "bmc", "ssh"} {
		row := models.LabValidationEvidence{Kind: kind, Subject: "old-" + kind, Status: "ok", Summary: "old evidence", CreatedBy: "tester@example.com", CreatedAt: oldTime}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create old evidence: %v", err)
		}
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	report := labValidationReport{Status: "ok"}
	h.fillLabEvidence(&report)
	if !labCheckContains(report.Checks, "physical_evidence", "warning", "missing fresh ok physical evidence") {
		t.Fatalf("expected stale evidence to warn, checks=%#v", report.Checks)
	}

	server := models.Server{AssetNo: "BM-EVIDENCE-FRESH", Hostname: "evidence-fresh", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create evidence server: %v", err)
	}
	bootEvent := models.BootEvent{MAC: "52:54:00:aa:42:00", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.42", Source: "http_ipxe"}
	if err := db.Create(&bootEvent).Error; err != nil {
		t.Fatalf("create fresh boot event: %v", err)
	}
	now := time.Now().UTC()
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.evidence.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create fresh bmc endpoint: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.42", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create fresh ssh access: %v", err)
	}
	run := createStrictFullEvidenceRun(t, db, server.ID, bootEvent.MAC, server.ID)
	fresh := models.LabValidationEvidence{
		Kind: "full", Subject: "rack-a validation run", Status: "ok", Summary: "fresh full-chain evidence", CreatedBy: "tester@example.com",
		RunID: &run.ID, ServerID: &server.ID, BootEventID: &bootEvent.ID, BmcEndpointID: &bmc.ID, SSHAccessID: &ssh.ID,
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("create fresh evidence: %v", err)
	}
	report = labValidationReport{Status: "ok"}
	h.fillLabEvidence(&report)
	if !labCheckContains(report.Checks, "physical_evidence", "ok", "within 7d") {
		t.Fatalf("expected fresh full evidence to pass, checks=%#v", report.Checks)
	}
	staleCheckedAt := time.Now().UTC().Add(-labEvidenceFreshnessWindow - time.Minute)
	if err := db.Model(&ssh).Updates(map[string]any{"last_checked_at": staleCheckedAt}).Error; err != nil {
		t.Fatalf("stale ssh reference: %v", err)
	}
	report = labValidationReport{Status: "ok"}
	h.fillLabEvidence(&report)
	if !labCheckContains(report.Checks, "physical_evidence", "warning", "missing fresh ok physical evidence") {
		t.Fatalf("expected stale referenced SSH check to invalidate full evidence, checks=%#v", report.Checks)
	}
}

func TestStrictFullChainTargetAcceptsPXEBootEventMatchedByMAC(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-full-chain-target?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "ssh", SSHOperationsMode: "ssh", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-FULL-CHAIN", Hostname: "full-chain", Status: "ready", Architecture: "x86_64", PrimaryMAC: "52-54-00-AA-BB-CC"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	bootEvent := models.BootEvent{MAC: "52:54:00:aa:bb:cc", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.50", Source: "http_ipxe", CreatedAt: now}
	if err := db.Create(&bootEvent).Error; err != nil {
		t.Fatalf("create boot event: %v", err)
	}
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.full-chain.test", Username: "admin", Status: "ok", PowerState: "on", LastCheckedAt: &now}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.51", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	terminal := models.TerminalSession{ServerID: server.ID, Status: "closed", Mode: "ssh", RequestedBy: "tester@example.com", Reason: "full-chain proof", Transcript: "printf terminal-ok\nterminal-ok\nexit_code=0", OpenedAt: now, ClosedAt: &now}
	if err := db.Create(&terminal).Error; err != nil {
		t.Fatalf("create terminal session: %v", err)
	}
	scriptJob := models.ScriptJob{Name: "ssh proof script", Status: "success", RequestedBy: "tester@example.com", StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&scriptJob).Error; err != nil {
		t.Fatalf("create script job: %v", err)
	}
	scriptExecution := models.ScriptExecution{ScriptJobID: scriptJob.ID, ServerID: server.ID, Status: "success", ExitCode: 0, Stdout: "script-ok", StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&scriptExecution).Error; err != nil {
		t.Fatalf("create script execution: %v", err)
	}
	logEvent := models.LogEvent{ServerID: server.ID, Source: "hardware", Level: "info", Message: "SSH hardware log collection for full-chain\nkernel: Linux lab 6.1", TraceID: "trace-full-chain", OccurredAt: now}
	if err := db.Create(&logEvent).Error; err != nil {
		t.Fatalf("create log event: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	targets, readyCount := h.labValidationTargets()
	if readyCount != 0 || len(targets) != 1 || targets[0].PXEStatus != "ok" || targets[0].PXEBootEventID == nil || *targets[0].PXEBootEventID != bootEvent.ID {
		t.Fatalf("expected PXE target to match boot event by primary MAC without full readiness: ready=%d targets=%#v", readyCount, targets)
	}
	results := h.strictFullChainTargetResults([]uint{server.ID})
	if len(results) != 1 || results[0].Kind != "full_chain_target" || results[0].Status != "failed" || !strings.Contains(results[0].Message, "full-chain evidence") {
		t.Fatalf("expected strict full-chain target to fail before evidence: %#v", results)
	}
	terminalRefs := h.labTerminalSessionsForBundle(map[uint]bool{server.ID: true})
	if len(terminalRefs) != 1 || terminalRefs[0].Mode != "ssh" || !strings.Contains(terminalRefs[0].Transcript, "exit_code=0") {
		t.Fatalf("expected lab evidence bundle terminal transcript ref: %#v", terminalRefs)
	}
	scriptRefs := h.labScriptExecutionsForBundle(map[uint]bool{server.ID: true})
	if len(scriptRefs) != 1 || scriptRefs[0].ScriptJobID != scriptJob.ID || scriptRefs[0].JobName != "ssh proof script" || !strings.Contains(scriptRefs[0].Stdout, "script-ok") {
		t.Fatalf("expected lab evidence bundle script execution ref: %#v", scriptRefs)
	}
	logRefs := h.labLogEventsForBundle(map[uint]bool{server.ID: true})
	if len(logRefs) != 1 || logRefs[0].Source != "hardware" || !strings.Contains(logRefs[0].Message, "SSH hardware log collection") {
		t.Fatalf("expected lab evidence bundle log event ref: %#v", logRefs)
	}
	evidence := models.LabValidationEvidence{Kind: "full", Subject: "full-chain", Status: "ok", Summary: "full-chain physical evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&evidence, nil, &server.ID, &bootEvent.ID); err != nil {
		if !strings.Contains(err.Error(), "run_id is required") {
			t.Fatalf("expected missing run_id to reject full evidence, got: %v", err)
		}
	} else {
		t.Fatalf("expected missing run_id to reject full evidence")
	}
	weakRun := createStrictFullEvidenceRun(t, db, server.ID, bootEvent.MAC, 0)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind IN ?", weakRun.ID, []string{"bmc", "ssh"}).
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak strict run proof details: %v", err)
	}
	weakEvidence := models.LabValidationEvidence{
		Kind: "full", Subject: "weak-full-chain", Status: "ok", Summary: "historical weak full-chain evidence", CreatedBy: "tester@example.com",
		RunID: &weakRun.ID, ServerID: &server.ID, BootEventID: &bootEvent.ID, BmcEndpointID: &bmc.ID, SSHAccessID: &ssh.ID, CreatedAt: now,
	}
	if err := db.Create(&weakEvidence).Error; err != nil {
		t.Fatalf("create weak full evidence: %v", err)
	}
	results = h.strictFullChainTargetResults([]uint{server.ID})
	if len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Message, "full-chain evidence") {
		t.Fatalf("expected weak historical full evidence without proof details to keep target blocked: %#v", results)
	}

	run := createStrictFullEvidenceRun(t, db, server.ID, bootEvent.MAC, 0)
	detail, err := h.labValidationRunDetail(run.ID)
	if err != nil {
		t.Fatalf("load strict run detail before evidence: %v", err)
	}
	bundle := h.buildLabValidationEvidenceBundle(detail)
	for _, kind := range []string{"pxe", "bmc", "ssh", "full"} {
		if !labEvidenceCandidateHas(bundle.EvidenceCandidates, kind, server.ID, bootEvent.ID) {
			t.Fatalf("expected evidence bundle to expose %s candidate before full evidence is recorded: %#v", kind, bundle.EvidenceCandidates)
		}
	}
	evidence = models.LabValidationEvidence{Kind: "full", Subject: "full-chain", Status: "ok", Summary: "full-chain physical evidence", CreatedBy: "tester@example.com"}
	if err := h.attachLabEvidenceReferences(&evidence, &run.ID, &server.ID, &bootEvent.ID); err != nil {
		t.Fatalf("attach full evidence by MAC-matched boot event and strict run: %v", err)
	}
	if evidence.ServerID == nil || *evidence.ServerID != server.ID || evidence.BootEventID == nil || *evidence.BootEventID != bootEvent.ID {
		t.Fatalf("expected full evidence to reference server and boot event: %#v", evidence)
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("create full evidence: %v", err)
	}
	results = h.strictFullChainTargetResults([]uint{server.ID})
	if len(results) != 1 || results[0].Status != "success" {
		t.Fatalf("expected strict full-chain target to pass after evidence: %#v", results)
	}
	detail, err = h.labValidationRunDetail(run.ID)
	if err != nil {
		t.Fatalf("load strict run detail: %v", err)
	}
	bundle = h.buildLabValidationEvidenceBundle(detail)
	if !labChecklistHas(bundle.OperatorChecklist, server.ID, "pxe_boot_event", "ok") ||
		!labChecklistHas(bundle.OperatorChecklist, server.ID, "bmc_identity", "ok") ||
		!labChecklistHas(bundle.OperatorChecklist, server.ID, "ssh_command", "ok") ||
		!labChecklistHas(bundle.OperatorChecklist, server.ID, "full_chain_evidence", "ok") {
		t.Fatalf("expected operator checklist to capture full physical proof path: %#v", bundle.OperatorChecklist)
	}
}

func TestLabEvidenceAPIRequiresStrictFullChainRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-full-evidence-api?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "ssh", SSHOperationsMode: "ssh", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-FULL-EVIDENCE-API", Hostname: "full-evidence-api", Status: "ready", Architecture: "x86_64", PrimaryMAC: "52:54:00:aa:66:01"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	bootEvent := models.BootEvent{MAC: "52:54:00:aa:66:01", Architecture: "x86_64", Firmware: "uefi", RemoteAddr: "192.0.2.66", Source: "http_ipxe", ServerID: &server.ID, CreatedAt: now}
	if err := db.Create(&bootEvent).Error; err != nil {
		t.Fatalf("create boot event: %v", err)
	}
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.full-evidence-api.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.67", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}

	payload := map[string]any{
		"kind": "full", "subject": "full-evidence-api", "status": "ok", "summary": "physical full-chain validation",
		"server_id": server.ID, "boot_event_id": bootEvent.ID,
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, payload, http.StatusBadRequest, "system.lab-validation.evidence")

	finished := now
	nonStrictRun := models.LabValidationRun{
		Status: "ok", Strict: false, CheckPXE: true, CheckBMC: true, CheckSSH: true, Limit: 20,
		ServerIDs: labJSONValue([]uint{server.ID}), PXEMACs: labJSONValue([]string{bootEvent.MAC}),
		PXEArch: 9, RequestedBy: "tester@example.com", RequestID: "non-strict-full-evidence", StartedAt: now, FinishedAt: &finished,
	}
	if err := db.Create(&nonStrictRun).Error; err != nil {
		t.Fatalf("create non-strict run: %v", err)
	}
	payload["run_id"] = nonStrictRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, payload, http.StatusBadRequest, "system.lab-validation.evidence")

	weakStrictRun := createStrictFullEvidenceRun(t, db, server.ID, bootEvent.MAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind IN ?", weakStrictRun.ID, []string{"bmc", "ssh"}).
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak strict run proof details: %v", err)
	}
	payload["run_id"] = weakStrictRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, payload, http.StatusBadRequest, "system.lab-validation.evidence")

	strictRun := createStrictFullEvidenceRun(t, db, server.ID, bootEvent.MAC, server.ID)
	payload["run_id"] = strictRun.ID
	evidence := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, payload, "system.lab-validation.evidence")
	if evidence["kind"] != "full" || intFromJSON(t, evidence, "run_id") != int(strictRun.ID) || intFromJSON(t, evidence, "server_id") != int(server.ID) || intFromJSON(t, evidence, "boot_event_id") != int(bootEvent.ID) {
		t.Fatalf("expected API to record full evidence with strict run references: %#v", evidence)
	}
}

func TestLabEvidenceAPIRequiresBMCAndSSHProofRuns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-bmc-ssh-evidence-api?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "ssh", SSHOperationsMode: "ssh", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-BMC-SSH-EVIDENCE", Hostname: "bmc-ssh-evidence", Status: "ready", Architecture: "x86_64", PrimaryMAC: "52:54:00:aa:77:01"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	bmc := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.bmc-ssh-evidence.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&bmc).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	ssh := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.77", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}

	bmcPayload := map[string]any{
		"kind": "bmc", "subject": "bmc-ssh-evidence redfish", "status": "ok", "summary": "physical BMC identity evidence",
		"server_id": server.ID,
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, bmcPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	finished := now
	nonStrictRun := models.LabValidationRun{
		Status: "ok", Strict: false, CheckPXE: false, CheckBMC: true, CheckSSH: true, Limit: 20,
		ServerIDs: labJSONValue([]uint{server.ID}), PXEMACs: labJSONValue([]string{}),
		PXEArch: 9, RequestedBy: "tester@example.com", RequestID: "non-strict-bmc-ssh-evidence", StartedAt: now, FinishedAt: &finished,
	}
	if err := db.Create(&nonStrictRun).Error; err != nil {
		t.Fatalf("create non-strict run: %v", err)
	}
	bmcPayload["run_id"] = nonStrictRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, bmcPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	weakBMCRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakBMCRun.ID, "bmc").
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak BMC proof details: %v", err)
	}
	bmcPayload["run_id"] = weakBMCRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, bmcPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	strictBMCRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	bmcPayload["run_id"] = strictBMCRun.ID
	bmcEvidence := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, bmcPayload, "system.lab-validation.evidence")
	if bmcEvidence["kind"] != "bmc" || intFromJSON(t, bmcEvidence, "run_id") != int(strictBMCRun.ID) || intFromJSON(t, bmcEvidence, "server_id") != int(server.ID) {
		t.Fatalf("expected API to record BMC evidence with strict proof run references: %#v", bmcEvidence)
	}

	sshPayload := map[string]any{
		"kind": "ssh", "subject": "192.0.2.77", "status": "ok", "summary": "physical SSH command proof",
		"server_id": server.ID,
	}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, sshPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	weakSSHRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakSSHRun.ID, "ssh").
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak SSH proof details: %v", err)
	}
	sshPayload["run_id"] = weakSSHRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, sshPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	weakSSHNoStdoutRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakSSHNoStdoutRun.ID, "ssh").
		Update("details", datatypes.JSON([]byte(`{"command":"true","exit_code":0}`))).Error; err != nil {
		t.Fatalf("set weak SSH proof without stdout: %v", err)
	}
	sshPayload["run_id"] = weakSSHNoStdoutRun.ID
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, sshPayload, http.StatusBadRequest, "system.lab-validation.evidence")

	strictSSHRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	sshPayload["run_id"] = strictSSHRun.ID
	sshEvidence := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/system/lab-validation/evidence", token, sshPayload, "system.lab-validation.evidence")
	if sshEvidence["kind"] != "ssh" || intFromJSON(t, sshEvidence, "run_id") != int(strictSSHRun.ID) || intFromJSON(t, sshEvidence, "server_id") != int(server.ID) {
		t.Fatalf("expected API to record SSH evidence with strict proof run references: %#v", sshEvidence)
	}
}

func TestHistoricalBMCAndSSHEvidenceRequiresProofRuns(t *testing.T) {
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:lab-historical-bmc-ssh-proof?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "redfish", CollectorMode: "ssh", SSHOperationsMode: "ssh", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	now := time.Now().UTC()
	server := models.Server{AssetNo: "BM-HISTORICAL-PROOF", Hostname: "historical-proof", Status: "ready", Architecture: "x86_64", PrimaryMAC: "52:54:00:aa:88:01"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: server.ID, Type: "redfish", Protocol: "https", Endpoint: "https://bmc.historical-proof.test", Username: "admin", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	access := models.SSHAccess{ServerID: server.ID, Host: "192.0.2.88", Port: 22, Username: "root", AuthType: "password", Status: "ok", LastCheckedAt: &now}
	if err := db.Create(&access).Error; err != nil {
		t.Fatalf("create ssh access: %v", err)
	}
	h := Handler{db: db, cfg: cfg, bmc: services.NewBMCService(db, cfg)}
	freshCutoff := now.Add(-labEvidenceFreshnessWindow)

	weakBMCRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakBMCRun.ID, "bmc").
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak BMC proof details: %v", err)
	}
	weakBMC := models.LabValidationEvidence{Kind: "bmc", Subject: "historical weak bmc", Status: "ok", Summary: "old weak bmc evidence", CreatedBy: "tester@example.com", RunID: &weakBMCRun.ID, ServerID: &server.ID, BmcEndpointID: &endpoint.ID, CreatedAt: now}
	if h.labEvidenceCountsAsFreshOK(weakBMC, freshCutoff) {
		t.Fatalf("historical BMC evidence without structured run proof must not count as fresh ok")
	}
	strictBMCRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	goodBMC := models.LabValidationEvidence{Kind: "bmc", Subject: "historical good bmc", Status: "ok", Summary: "old good bmc evidence", CreatedBy: "tester@example.com", RunID: &strictBMCRun.ID, ServerID: &server.ID, BmcEndpointID: &endpoint.ID, CreatedAt: now}
	if !h.labEvidenceCountsAsFreshOK(goodBMC, freshCutoff) {
		t.Fatalf("BMC evidence with structured run proof should count as fresh ok")
	}

	weakSSHRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakSSHRun.ID, "ssh").
		Update("details", datatypes.JSON(nil)).Error; err != nil {
		t.Fatalf("clear weak SSH proof details: %v", err)
	}
	weakSSH := models.LabValidationEvidence{Kind: "ssh", Subject: "historical weak ssh", Status: "ok", Summary: "old weak ssh evidence", CreatedBy: "tester@example.com", RunID: &weakSSHRun.ID, ServerID: &server.ID, SSHAccessID: &access.ID, CreatedAt: now}
	if h.labEvidenceCountsAsFreshOK(weakSSH, freshCutoff) {
		t.Fatalf("historical SSH evidence without command proof must not count as fresh ok")
	}
	weakSSHNoStdoutRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	if err := db.Model(&models.LabValidationRunResult{}).
		Where("run_id = ? AND kind = ?", weakSSHNoStdoutRun.ID, "ssh").
		Update("details", datatypes.JSON([]byte(`{"command":"true","exit_code":0}`))).Error; err != nil {
		t.Fatalf("set weak historical SSH proof without stdout: %v", err)
	}
	weakSSHNoStdout := models.LabValidationEvidence{Kind: "ssh", Subject: "historical weak ssh no stdout", Status: "ok", Summary: "old weak ssh evidence", CreatedBy: "tester@example.com", RunID: &weakSSHNoStdoutRun.ID, ServerID: &server.ID, SSHAccessID: &access.ID, CreatedAt: now}
	if h.labEvidenceCountsAsFreshOK(weakSSHNoStdout, freshCutoff) {
		t.Fatalf("historical SSH evidence without stdout proof must not count as fresh ok")
	}
	strictSSHRun := createStrictFullEvidenceRun(t, db, server.ID, server.PrimaryMAC, server.ID)
	goodSSH := models.LabValidationEvidence{Kind: "ssh", Subject: "historical good ssh", Status: "ok", Summary: "old good ssh evidence", CreatedBy: "tester@example.com", RunID: &strictSSHRun.ID, ServerID: &server.ID, SSHAccessID: &access.ID, CreatedAt: now}
	if !h.labEvidenceCountsAsFreshOK(goodSSH, freshCutoff) {
		t.Fatalf("SSH evidence with command proof should count as fresh ok")
	}
}

func TestMetricRetentionWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:metric-retention?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-METRIC-RETENTION", Hostname: "metric-retention", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	oldMetric := models.MetricSample{ServerID: server.ID, MetricName: "cpu_usage", Value: 99, Unit: "%", CollectedAt: time.Now().UTC().Add(-8 * 24 * time.Hour)}
	if err := db.Create(&oldMetric).Error; err != nil {
		t.Fatalf("create old metric: %v", err)
	}
	rule := models.AlertRule{RuleID: "cpu.retention", Name: "CPU retention", MetricName: "cpu_usage", Operator: ">", Threshold: 90, Severity: "warning", Status: "enabled", CreatedBy: "test"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}

	metrics := requestJSON(t, r, http.MethodGet, "/api/v1/servers/"+itoa(int(server.ID))+"/metrics", token, nil)
	if jsonArrayContainsField(metrics["_array"].([]any), "value", float64(99)) {
		t.Fatalf("old metric should not be returned from retention-limited query: %#v", metrics)
	}
	evaluated := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules/evaluate", token, map[string]any{})
	if intFromJSON(t, evaluated, "created") != 0 {
		t.Fatalf("old metric should not trigger alert evaluation: %#v", evaluated)
	}

	requestJSON(t, r, http.MethodPost, "/api/v1/servers/"+itoa(int(server.ID))+"/collections", token, map[string]any{})
	time.Sleep(500 * time.Millisecond)
	var oldCount int64
	if err := db.Model(&models.MetricSample{}).Where("id = ?", oldMetric.ID).Count(&oldCount).Error; err != nil {
		t.Fatalf("count old metric: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old metric should be pruned after successful collection")
	}
}

func TestAlertEvaluationDeduplicatesAndRetriggersResolvedAlerts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:alert-dedup?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-ALERT-DEDUP", Hostname: "alert-dedup", Status: "running", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	rule := models.AlertRule{RuleID: "test.cpu.dedup", Name: "CPU dedup", MetricName: "cpu_usage", Operator: ">", Threshold: 80, Severity: "warning", Status: "enabled", CreatedBy: "test"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	metric := models.MetricSample{ServerID: server.ID, MetricName: "cpu_usage", Value: 95, Unit: "%", CollectedAt: time.Now().UTC()}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("create metric: %v", err)
	}

	firstEvaluation := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules/evaluate", token, map[string]any{})
	if intFromJSON(t, firstEvaluation, "created") != 1 || intFromJSON(t, firstEvaluation, "deduplicated") != 0 {
		t.Fatalf("expected first evaluation to create one alert: %#v", firstEvaluation)
	}
	alerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(int(server.ID))+"&rule_id=test.cpu.dedup&page=1&page_size=10", token, nil)
	if intFromJSON(t, alerts, "total") != 1 {
		t.Fatalf("expected one alert after first evaluation: %#v", alerts)
	}
	alertID := int(alerts["items"].([]any)[0].(map[string]any)["id"].(float64))

	refreshedMetric := models.MetricSample{ServerID: server.ID, MetricName: "cpu_usage", Value: 97, Unit: "%", CollectedAt: time.Now().UTC().Add(time.Second)}
	if err := db.Create(&refreshedMetric).Error; err != nil {
		t.Fatalf("create refreshed metric: %v", err)
	}
	secondEvaluation := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules/evaluate", token, map[string]any{})
	if intFromJSON(t, secondEvaluation, "created") != 0 || intFromJSON(t, secondEvaluation, "deduplicated") != 1 {
		t.Fatalf("expected repeated evaluation to deduplicate active alert: %#v", secondEvaluation)
	}
	alerts = requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(int(server.ID))+"&rule_id=test.cpu.dedup&page=1&page_size=10", token, nil)
	if intFromJSON(t, alerts, "total") != 1 {
		t.Fatalf("active alert should not be duplicated: %#v", alerts)
	}
	activeAlert := alerts["items"].([]any)[0].(map[string]any)
	if !contains(activeAlert["description"].(string), "97.00") {
		t.Fatalf("deduplicated alert should refresh latest metric value: %#v", activeAlert)
	}

	requestJSON(t, r, http.MethodPost, "/api/v1/alerts/"+itoa(alertID)+"/resolve", token, map[string]any{"note": "cpu recovered manually"})
	retriggerEvaluation := requestJSON(t, r, http.MethodPost, "/api/v1/alert-rules/evaluate", token, map[string]any{})
	if intFromJSON(t, retriggerEvaluation, "created") != 1 || intFromJSON(t, retriggerEvaluation, "deduplicated") != 0 {
		t.Fatalf("resolved alert should retrigger as a new firing alert: %#v", retriggerEvaluation)
	}
	ruleAlerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(int(server.ID))+"&rule_id=test.cpu.dedup&page=1&page_size=10", token, nil)
	if intFromJSON(t, ruleAlerts, "total") != 2 {
		t.Fatalf("expected resolved history plus new firing alert: %#v", ruleAlerts)
	}
	firingAlerts := requestJSON(t, r, http.MethodGet, "/api/v1/alerts?server_id="+itoa(int(server.ID))+"&rule_id=test.cpu.dedup&status=firing&page=1&page_size=10", token, nil)
	if intFromJSON(t, firingAlerts, "total") != 1 {
		t.Fatalf("expected exactly one firing alert after retrigger: %#v", firingAlerts)
	}
}

func TestScriptJobHonorsConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:script-concurrency?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	servers := []models.Server{
		{AssetNo: "BM-SCRIPT-001", Hostname: "script-1", Status: "ready", Architecture: "x86_64"},
		{AssetNo: "BM-SCRIPT-002", Hostname: "script-2", Status: "ready", Architecture: "x86_64"},
		{AssetNo: "BM-SCRIPT-003", Hostname: "script-3", Status: "ready", Architecture: "x86_64"},
	}
	if err := db.Create(&servers).Error; err != nil {
		t.Fatalf("create servers: %v", err)
	}
	job := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/script-jobs", token, map[string]any{"name": "Serial script", "script": "uptime", "server_ids": []uint{servers[0].ID, servers[1].ID, servers[2].ID}, "concurrency": 1, "timeout_seconds": 30}, "ops.script.create")
	jobID := intFromJSON(t, job, "id")
	time.Sleep(250 * time.Millisecond)
	earlyResults := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs/"+itoa(jobID)+"/results", token, nil)
	if statusCount(earlyResults["_array"].([]any), "success") == 3 {
		t.Fatalf("concurrency=1 should not finish all executions in the first batch: %#v", earlyResults)
	}
	time.Sleep(1 * time.Second)
	finalResults := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs/"+itoa(jobID)+"/results", token, nil)
	if statusCount(finalResults["_array"].([]any), "success") != 3 {
		t.Fatalf("expected all script executions to finish successfully: %#v", finalResults)
	}
	finalJob := requestJSON(t, r, http.MethodGet, "/api/v1/ops/script-jobs/"+itoa(jobID), token, nil)
	if finalJob["status"] != "success" {
		t.Fatalf("expected script job success after all batches: %#v", finalJob)
	}
}

func TestTerminalSessionTTLClosesExpiredSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:terminal-ttl?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, TerminalSessionTTL: time.Minute, EnableDemoSeeder: false,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)
	token := loginForTest(t, r)

	server := models.Server{AssetNo: "BM-TERMINAL-TTL", Hostname: "terminal-ttl", Status: "ready", Architecture: "x86_64"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	terminal := requestJSONWithConfirm(t, r, http.MethodPost, "/api/v1/ops/terminal-sessions", token, map[string]any{"server_id": server.ID, "reason": "ttl test"}, "ops.terminal.open")
	terminalID := intFromJSON(t, terminal, "id")
	staleOpenedAt := time.Now().UTC().Add(-2 * time.Minute)
	if err := db.Model(&models.TerminalSession{}).Where("id = ?", terminalID).Update("opened_at", staleOpenedAt).Error; err != nil {
		t.Fatalf("age terminal session: %v", err)
	}

	filtered := requestJSON(t, r, http.MethodGet, "/api/v1/ops/terminal-sessions?status=closed&page=1&page_size=10", token, nil)
	if intFromJSON(t, filtered, "total") != 1 {
		t.Fatalf("expected expired terminal session to be listed as closed: %#v", filtered)
	}
	detail := requestJSON(t, r, http.MethodGet, "/api/v1/ops/terminal-sessions/"+itoa(terminalID), token, nil)
	if detail["status"] != "closed" || !contains(detail["transcript"].(string), "auto-closed after 1 minute TTL") {
		t.Fatalf("expected terminal session to be auto-closed with transcript note: %#v", detail)
	}
	if detail["closed_at"] == nil || detail["closed_at"] == "" {
		t.Fatalf("expected closed_at to be set after terminal TTL cleanup: %#v", detail)
	}

	var auditCount int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "ops.terminal.auto_close", "terminal_session", itoa(terminalID)).Count(&auditCount).Error; err != nil {
		t.Fatalf("count terminal auto close audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one terminal auto-close audit, got %d", auditCount)
	}
	requestJSON(t, r, http.MethodGet, "/api/v1/ops/terminal-sessions/"+itoa(terminalID), token, nil)
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "ops.terminal.auto_close", "terminal_session", itoa(terminalID)).Count(&auditCount).Error; err != nil {
		t.Fatalf("count terminal auto close audits after repeated read: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected terminal auto-close audit to remain idempotent, got %d", auditCount)
	}
}

func TestLoginRateLimitBlocksRepeatedFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:login-rate-limit?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, EnableDemoSeeder: false,
		LoginRateAttempts: 2, LoginRateWindow: time.Minute,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)

	payload := map[string]any{"email": "admin@example.com", "password": "wrong-password"}
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/auth/login", "", payload, http.StatusUnauthorized, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/auth/login", "", payload, http.StatusUnauthorized, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/auth/login", "", payload, http.StatusTooManyRequests, "")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "admin@example.com", "password": "Admin@123456"}, http.StatusTooManyRequests, "")
}

func TestMetadataAccessRequiresDeploymentNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{
		AppEnv: "test", HTTPAddr: ":0", JWTSecret: "test-secret", TokenTTL: time.Hour,
		DBDriver: "sqlite", DatabaseURL: "file:metadata-strict?mode=memory&cache=shared", AdminEmail: "admin@example.com", AdminPassword: "Admin@123456", CredentialKey: "test-credential-key", BMCAdapter: "simulated", CollectorMode: "simulated", BootBaseURL: "http://boot.test", ImageStorageDir: t.TempDir(), ImageUploadMaxBytes: 1024 * 1024, MetadataRequireDeploy: true, EnableDemoSeeder: true,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Fatalf("database init: %v", err)
	}
	r := NewRouter(db, cfg)

	var server models.Server
	if err := db.Where("hostname = ?", "demo-node-01").First(&server).Error; err != nil {
		t.Fatalf("load demo server: %v", err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	metadataToken := models.MetadataToken{Token: "strict-metadata-token", ServerID: server.ID, ExpiresAt: &expires}
	if err := db.Create(&metadataToken).Error; err != nil {
		t.Fatalf("create metadata token: %v", err)
	}

	requestExpectStatusWithRemote(t, r, http.MethodGet, "/metadata/by-server/"+itoa(int(server.ID))+"/hostname", "", nil, "203.0.113.10:1234", http.StatusForbidden, "")
	requestExpectStatusWithRemote(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken.Token+"/hostname", "", nil, "203.0.113.10:1234", http.StatusForbidden, "")
	hostname := requestTextWithRemote(t, r, http.MethodGet, "/metadata/by-token/"+metadataToken.Token+"/hostname", "", nil, "192.168.100.55:1234")
	if hostname != server.Hostname {
		t.Fatalf("expected metadata hostname %q from deployment network, got %q", server.Hostname, hostname)
	}

	var deniedLogs []models.LogEvent
	db.Where("source = ? AND level = ?", "metadata", "warning").Find(&deniedLogs)
	if len(deniedLogs) < 2 {
		t.Fatalf("expected metadata denial logs, got %#v", deniedLogs)
	}
	for _, log := range deniedLogs {
		if strings.Contains(log.Message, metadataToken.Token) || strings.Contains(log.TraceID, metadataToken.Token) {
			t.Fatalf("metadata denial log leaked token: %#v", log)
		}
	}
}

func loginForTest(t *testing.T, r http.Handler) string {
	return loginForTestWith(t, r, "admin@example.com", "Admin@123456")
}

func loginForTestWith(t *testing.T, r http.Handler, email string, password string) string {
	body := requestJSON(t, r, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": email, "password": password})
	token, ok := body["token"].(string)
	if !ok || token == "" {
		t.Fatalf("login did not return token: %#v", body)
	}
	return token
}

func requestText(t *testing.T, r http.Handler, method, path, token string, payload any) string {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code < 200 || res.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, res.Code, res.Body.String())
	}
	text, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func requestTextWithRemote(t *testing.T, r http.Handler, method, path, token string, payload any, remoteAddr string) string {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code < 200 || res.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, res.Code, res.Body.String())
	}
	text, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func requestJSON(t *testing.T, r http.Handler, method, path, token string, payload any) map[string]any {
	t.Helper()
	body, _ := requestJSONWithHeaders(t, r, method, path, token, payload, nil)
	return body
}

func requestJSONWithHeaders(t *testing.T, r http.Handler, method, path, token string, payload any, headers map[string]string) (map[string]any, http.Header) {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code < 200 || res.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, res.Code, res.Body.String())
	}
	var decoded any
	if res.Body.Len() == 0 {
		return map[string]any{}, res.Header()
	}
	if err := json.Unmarshal(res.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if arr, ok := decoded.([]any); ok {
		return map[string]any{"_array": arr}, res.Header()
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("unexpected json response: %#v", decoded)
	}
	return obj, res.Header()
}

func requestJSONWithConfirm(t *testing.T, r http.Handler, method, path, token string, payload any, confirm string) map[string]any {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Confirm-Action", confirm)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code < 200 || res.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, res.Code, res.Body.String())
	}
	var decoded any
	if res.Body.Len() == 0 {
		return map[string]any{}
	}
	if err := json.Unmarshal(res.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if arr, ok := decoded.([]any); ok {
		return map[string]any{"_array": arr}
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("unexpected json response: %#v", decoded)
	}
	return obj
}

func requestMultipartImage(t *testing.T, r http.Handler, token string, fields map[string]string, filename string, content []byte) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code < 200 || res.Code >= 300 {
		t.Fatalf("POST /api/v1/images/upload returned %d: %s", res.Code, res.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	return obj
}

func requestExpectStatus(t *testing.T, r http.Handler, method, path, token string, payload any, status int, confirm string) {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", "application/json")
	if confirm != "" {
		req.Header.Set("X-Confirm-Action", confirm)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != status {
		t.Fatalf("%s %s returned %d, expected %d: %s", method, path, res.Code, status, res.Body.String())
	}
}

func requestExpectStatusWithRemote(t *testing.T, r http.Handler, method, path, token string, payload any, remoteAddr string, status int, confirm string) {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	if confirm != "" {
		req.Header.Set("X-Confirm-Action", confirm)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != status {
		t.Fatalf("%s %s returned %d, expected %d: %s", method, path, res.Code, status, res.Body.String())
	}
}

func requestJSONExpectStatus(t *testing.T, r http.Handler, method, path, token string, payload any, status int, confirm string) map[string]any {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", "application/json")
	if confirm != "" {
		req.Header.Set("X-Confirm-Action", confirm)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != status {
		t.Fatalf("%s %s returned %d, expected %d: %s", method, path, res.Code, status, res.Body.String())
	}
	var decoded any
	if res.Body.Len() == 0 {
		return map[string]any{}
	}
	if err := json.Unmarshal(res.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("unexpected json response: %#v", decoded)
	}
	return obj
}

func intFromJSON(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("missing numeric field %s in %#v", key, m)
	}
	return int(v)
}

func waitForDeploymentStatus(t *testing.T, r http.Handler, token string, deploymentID int, timeout time.Duration, statuses ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var row map[string]any
	for time.Now().Before(deadline) {
		row = requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentID), token, nil)
		status, _ := row["status"].(string)
		if stringIn(status, statuses...) {
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("deployment %d did not reach %v within %s; last row: %#v", deploymentID, statuses, timeout, row)
	return nil
}

func waitForOneRunningOnePending(t *testing.T, r http.Handler, token string, firstID int, secondID int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var first map[string]any
	var second map[string]any
	for time.Now().Before(deadline) {
		first = requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(firstID), token, nil)
		second = requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(secondID), token, nil)
		firstStatus, _ := first["status"].(string)
		secondStatus, _ := second["status"].(string)
		if (firstStatus == "running" && secondStatus == "pending") || (firstStatus == "pending" && secondStatus == "running") {
			return
		}
		if firstStatus == "success" && secondStatus == "success" {
			t.Fatalf("both deployments completed before queue state was observed: first=%#v second=%#v", first, second)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("deployments did not show one running and one pending within %s; first=%#v second=%#v", timeout, first, second)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func tempImageFile(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(dir, "baremetal-test-*.iso")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("test image content"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	return file.Name()
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen free UDP port: %v", err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("close free UDP probe socket: %v", err)
	}
	return addr
}

func contains(text, needle string) bool {
	if needle == "" {
		return true
	}
	if len(needle) > len(text) {
		return false
	}
	for i := 0; i <= len(text)-len(needle); i++ {
		if text[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func jsonArrayContainsAction(rows []any, action string) bool {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if ok && obj["action"] == action {
			return true
		}
	}
	return false
}

func jsonArrayContainsString(rows []any, needle string) bool {
	for _, row := range rows {
		if value, ok := row.(string); ok && value == needle {
			return true
		}
	}
	return false
}

func jsonArrayContainsField(rows []any, key string, expected any) bool {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if ok && obj[key] == expected {
			return true
		}
	}
	return false
}

func jsonArrayFindByField(rows []any, key string, expected any) (map[string]any, bool) {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if ok && obj[key] == expected {
			return obj, true
		}
	}
	return nil, false
}

func jsonCheckContains(rows []any, name string, status string, messageNeedle string) bool {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok || obj["name"] != name || obj["status"] != status {
			continue
		}
		message, _ := obj["message"].(string)
		if strings.Contains(message, messageNeedle) {
			return true
		}
	}
	return false
}

func statusCount(rows []any, status string) int {
	count := 0
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if ok && obj["status"] == status {
			count++
		}
	}
	return count
}

func jsonArrayFieldContains(rows []any, key string, needle string) bool {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok {
			continue
		}
		value, ok := obj[key].(string)
		if ok && contains(value, needle) {
			return true
		}
	}
	return false
}

func labCheckContains(checks []labValidationCheck, name string, status string, messageNeedle string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status && strings.Contains(check.Message, messageNeedle) {
			return true
		}
	}
	return false
}

func labChecklistHas(items []labOperatorChecklistItem, serverID uint, step string, status string) bool {
	for _, item := range items {
		if item.ServerID == serverID && item.Step == step && item.Status == status {
			return true
		}
	}
	return false
}

func labEvidenceCandidateHas(items []labEvidenceCandidate, kind string, serverID uint, bootEventID uint) bool {
	for _, item := range items {
		if item.Kind != kind {
			continue
		}
		switch kind {
		case "pxe":
			if item.BootEventID == bootEventID && (item.ServerID == 0 || item.ServerID == serverID) {
				return true
			}
		case "bmc", "ssh":
			if item.ServerID == serverID {
				return true
			}
		case "full":
			if item.ServerID == serverID && item.BootEventID == bootEventID {
				return true
			}
		}
	}
	return false
}

func createStrictFullEvidenceRun(t *testing.T, db *gorm.DB, serverID uint, mac string, pxeResultServerID uint) models.LabValidationRun {
	t.Helper()
	now := time.Now().UTC()
	run := models.LabValidationRun{
		Status: "error", Strict: true, CheckPXE: true, CheckBMC: true, CheckSSH: true, Limit: 20,
		ServerIDs: labJSONValue([]uint{serverID}), PXEMACs: labJSONValue([]string{mac}),
		PXEArch: 9, SSHProbeCommand: services.DefaultSSHCheckCommand, RequestedBy: "tester@example.com",
		RequestID: "test-full-chain-run", StartedAt: now, FinishedAt: &now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create strict full evidence run: %v", err)
	}
	results := []models.LabValidationRunResult{
		{RunID: run.ID, Kind: "pxe_boot_event", ServerID: pxeResultServerID, AssetNo: mac, Status: "success", Message: "PXE boot event for " + mac + " recorded via http_ipxe", CheckedAt: &now},
		{RunID: run.ID, Kind: "bmc", ServerID: serverID, Status: "success", Message: "BMC connectivity and identity probe passed", Details: labBMCFirmwareDetails(services.BMCFirmwareInfo{Adapter: "redfish", Manufacturer: "Dell", Model: "PowerEdge R650", SerialNumber: "LAB-SERIAL-001"}), CheckedAt: &now},
		{RunID: run.ID, Kind: "ssh", ServerID: serverID, Status: "success", Message: "SSH probe passed: exit_code=0; host_key_policy=known_hosts; host_key_verified=true", Details: labRemoteCommandDetails(services.DefaultSSHCheckCommand, services.RemoteCommandResult{ExitCode: 0, Stdout: "ok lab-ssh tester Linux x86_64", HostKeyPolicy: "known_hosts", HostKeyVerified: true, HostKeyAlgorithm: "ssh-ed25519", HostKeySHA256: "SHA256:testfingerprint", HostKeyHost: "[192.0.2.88]:22", HostKeyRemote: "192.0.2.88:22"}, nil), CheckedAt: &now},
		{RunID: run.ID, Kind: "full_chain_target", ServerID: serverID, Status: "failed", Message: "full-chain target is not ready: full-chain evidence is not fresh ok", CheckedAt: &now},
	}
	if err := db.Create(&results).Error; err != nil {
		t.Fatalf("create strict full evidence run results: %v", err)
	}
	return run
}

func idByField(t *testing.T, rows []any, key string, expected any) int {
	t.Helper()
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if ok && obj[key] == expected {
			if id, ok := obj["id"].(float64); ok {
				return int(id)
			}
			t.Fatalf("row for %s=%v has no numeric id: %#v", key, expected, obj)
		}
	}
	t.Fatalf("no row found for %s=%v in %#v", key, expected, rows)
	return 0
}

func jsonArrayContainsBlankField(rows []any, key string) bool {
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok {
			return true
		}
		value, ok := obj[key].(string)
		if !ok || value == "" {
			return true
		}
	}
	return false
}

func copyMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func deploymentPayload(src map[string]any) map[string]any {
	dst := copyMap(src)
	if _, ok := dst["erase_policy"]; !ok {
		dst["erase_policy"] = "quick"
	}
	if _, ok := dst["erase_confirmed"]; !ok {
		dst["erase_confirmed"] = true
	}
	return dst
}

func cloneJSONMap(t *testing.T, src map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal cloned map: %v", err)
	}
	var dst map[string]any
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal cloned map: %v", err)
	}
	return dst
}

func metadataTokenFromIPXE(t *testing.T, script string) string {
	t.Helper()
	const marker = "/metadata/by-token/"
	idx := strings.Index(script, marker)
	if idx < 0 {
		t.Fatalf("missing metadata token URL in %s", script)
	}
	start := idx + len(marker)
	end := start
	for end < len(script) {
		ch := script[end]
		if ch == '\n' || ch == '\r' || ch == ' ' || ch == '\t' {
			break
		}
		end++
	}
	token := script[start:end]
	if token == "" {
		t.Fatalf("empty metadata token in %s", script)
	}
	return token
}

func queueWorkflowDefinition(steps int) string {
	if steps < 1 {
		steps = 1
	}
	var b strings.Builder
	b.WriteString(`{"steps":[`)
	for i := 1; i <= steps; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf(`{"name":"step %d","action":"install_os"}`, i))
	}
	b.WriteString(`]}`)
	return b.String()
}
