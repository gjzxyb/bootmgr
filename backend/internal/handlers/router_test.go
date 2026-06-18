package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
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
	readiness, readinessHeaders := requestJSONWithHeaders(t, r, http.MethodGet, "/readyz", "", nil, map[string]string{"X-Request-ID": "test-ready-001"})
	if readinessHeaders.Get("X-Request-ID") != "test-ready-001" {
		t.Fatalf("expected readyz X-Request-ID to be propagated, got %q", readinessHeaders.Get("X-Request-ID"))
	}
	checks, ok := readiness["checks"].([]any)
	if !ok {
		t.Fatalf("expected readyz checks array: %#v", readiness)
	}
	for _, name := range []string{"database", "redis", "image_storage", "config"} {
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

	networks := requestJSON(t, r, http.MethodGet, "/api/v1/network-configs?purpose=deployment&status=enabled&page=1&page_size=1", token, nil)
	if intFromJSON(t, networks, "total") != 1 {
		t.Fatalf("expected demo deployment network: %#v", networks)
	}
	networkItems := networks["items"].([]any)
	networkID := int(networkItems[0].(map[string]any)["id"].(float64))
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
	if networkCheck["status"] != "ok" || !jsonArrayContainsField(networkCheck["checks"].([]any), "name", "deployment_usage") {
		t.Fatalf("expected deployment network check to pass: %#v", networkCheck)
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
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "https", "endpoint": "192.0.2.55:623", "username": "ADMIN", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
	requestExpectStatus(t, r, http.MethodPost, "/api/v1/servers/"+itoa(serverID)+"/bmc", token, map[string]any{"type": "ipmi", "protocol": "ipmi", "endpoint": "bad host", "username": "ADMIN", "password": "secret"}, http.StatusBadRequest, "bmc.upsert")
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
	if checkResp["status"] != "error" || !contains(checkResp["detail"].(string), "returned 500") {
		t.Fatalf("expected failed BMC check to expose connectivity status: %#v", checkResp)
	}
	var failedCheckAudits int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND resource_type = ? AND resource_id = ?", "bmc.check", "server", itoa(int(server.ID))).Count(&failedCheckAudits).Error; err != nil {
		t.Fatalf("count failed bmc check audits: %v", err)
	}
	if failedCheckAudits != 1 {
		t.Fatalf("expected failed BMC check to be audited, got %d", failedCheckAudits)
	}

	deploymentResp := requestJSONExpectStatus(t, r, http.MethodPost, "/api/v1/deployments", token, deploymentPayload(map[string]any{"server_id": server.ID, "image_id": image.ID, "template_id": installTemplate.ID, "workflow_id": workflowTemplate.ID}), http.StatusBadRequest, "deployment.create")
	if !jsonArrayContainsString(deploymentResp["problems"].([]any), "bmc connectivity check failed: redfish GET /redfish/v1 returned 500") {
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
	workflowTemplate := models.WorkflowTemplate{Name: "Queue workflow", Version: "v1", Status: "enabled", Definition: datatypes.JSON([]byte(`{"steps":[{"name":"step 1","action":"install_os"},{"name":"step 2","action":"install_os"},{"name":"step 3","action":"install_os"},{"name":"step 4","action":"install_os"},{"name":"step 5","action":"install_os"},{"name":"step 6","action":"install_os"}]}`))}
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

	waitForDeploymentStatus(t, r, token, deploymentAID, 2*time.Second, "running")
	queued := requestJSON(t, r, http.MethodGet, "/api/v1/deployments/"+itoa(deploymentBID), token, nil)
	if queued["status"] != "pending" {
		t.Fatalf("second deployment should remain pending while the only worker is busy: %#v", queued)
	}
	waitForDeploymentStatus(t, r, token, deploymentAID, 6*time.Second, "success")
	waitForDeploymentStatus(t, r, token, deploymentBID, 6*time.Second, "success")
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
