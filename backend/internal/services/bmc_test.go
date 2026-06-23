package services

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"
)

func TestRedfishAdapterDiscoversCollectionsAndResetTarget(t *testing.T) {
	db := newCredentialTestDB(t)
	credentials := NewCredentialService(db, config.Config{CredentialKey: "redfish-test-credential-key"})
	cred, err := credentials.Store("redfish-password", "bmc", "admin", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store redfish credential: %v", err)
	}
	var resetTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1":
			_ = json.NewEncoder(w).Encode(map[string]any{"RedfishVersion": "1.15.0"})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems":
			_ = json.NewEncoder(w).Encode(map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Systems/System.Embedded.1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems/System.Embedded.1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"@odata.id":       "/redfish/v1/Systems/System.Embedded.1",
				"PowerState":      "On",
				"Manufacturer":    "Dell Inc.",
				"Model":           "PowerEdge R650",
				"SerialNumber":    "LAB1234",
				"BiosVersion":     "2.10.1",
				"FirmwareVersion": "system-fw",
				"Actions": map[string]any{
					"#ComputerSystem.Reset": map[string]string{"target": "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Managers":
			_ = json.NewEncoder(w).Encode(map[string]any{"Members": []map[string]string{{"@odata.id": "/redfish/v1/Managers/iDRAC.Embedded.1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Managers/iDRAC.Embedded.1":
			_ = json.NewEncoder(w).Encode(map[string]any{"FirmwareVersion": "7.10.30.00", "Model": "iDRAC9"})
		case r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset":
			var payload struct {
				ResetType string
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode reset payload: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			resetTypes = append(resetTypes, payload.ResetType)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := RedfishBMCAdapter{credentials: credentials, client: server.Client()}
	endpoint := models.BmcEndpoint{Endpoint: server.URL, Username: "admin", EncryptedPasswordRef: strconv.FormatUint(uint64(cred.ID), 10)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := adapter.Check(ctx, endpoint); err != nil {
		t.Fatalf("redfish check: %v", err)
	}
	state, err := adapter.PowerState(ctx, endpoint)
	if err != nil {
		t.Fatalf("redfish power state: %v", err)
	}
	if state != "on" {
		t.Fatalf("expected redfish power state on, got %q", state)
	}
	info, err := adapter.FirmwareInfo(ctx, endpoint)
	if err != nil {
		t.Fatalf("redfish firmware info: %v", err)
	}
	if info.Manufacturer != "Dell Inc." || info.Model != "PowerEdge R650" || info.SerialNumber != "LAB1234" || info.BMCVersion != "7.10.30.00" {
		t.Fatalf("unexpected redfish firmware info: %#v", info)
	}
	actual, err := adapter.SetPowerState(ctx, endpoint, "reboot")
	if err != nil {
		t.Fatalf("redfish reboot: %v", err)
	}
	if actual != "on" || !reflect.DeepEqual(resetTypes, []string{"ForceRestart"}) {
		t.Fatalf("unexpected redfish reset result actual=%q resetTypes=%v", actual, resetTypes)
	}
}

func TestRedfishAdapterResolvesServiceRootEndpointAndRelativeLinks(t *testing.T) {
	db := newCredentialTestDB(t)
	credentials := NewCredentialService(db, config.Config{CredentialKey: "redfish-relative-link-key"})
	cred, err := credentials.Store("redfish-password", "bmc", "admin", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store redfish credential: %v", err)
	}
	var resetPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1":
			_ = json.NewEncoder(w).Encode(map[string]any{"RedfishVersion": "1.15.0"})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems":
			_ = json.NewEncoder(w).Encode(map[string]any{"Members": []map[string]string{{"@odata.id": "Systems/System.1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/redfish/v1/Systems/System.1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"@odata.id":  "Systems/System.1",
				"PowerState": "On",
				"Actions": map[string]any{
					"#ComputerSystem.Reset": map[string]string{"target": "Actions/ComputerSystem.Reset"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/redfish/v1/Systems/System.1/Actions/ComputerSystem.Reset":
			resetPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := RedfishBMCAdapter{credentials: credentials, client: server.Client()}
	endpoint := models.BmcEndpoint{Endpoint: server.URL + "/redfish/v1", Username: "admin", EncryptedPasswordRef: strconv.FormatUint(uint64(cred.ID), 10)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := adapter.Check(ctx, endpoint); err != nil {
		t.Fatalf("redfish service root endpoint check: %v", err)
	}
	state, err := adapter.PowerState(ctx, endpoint)
	if err != nil {
		t.Fatalf("redfish relative power state: %v", err)
	}
	if state != "on" {
		t.Fatalf("expected redfish power state on, got %q", state)
	}
	if _, err := adapter.SetPowerState(ctx, endpoint, "reboot"); err != nil {
		t.Fatalf("redfish relative reboot: %v", err)
	}
	if resetPath != "/redfish/v1/Systems/System.1/Actions/ComputerSystem.Reset" {
		t.Fatalf("unexpected relative reset path %q", resetPath)
	}
}

func TestRedfishAdapterIncludesErrorResponseBody(t *testing.T) {
	db := newCredentialTestDB(t)
	credentials := NewCredentialService(db, config.Config{CredentialKey: "redfish-error-body-key"})
	cred, err := credentials.Store("redfish-password", "bmc", "admin", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store redfish credential: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "Base.1.16.ServiceTemporarilyUnavailable",
				"message": "BMC is rebooting",
				"@Message.ExtendedInfo": []map[string]string{
					{"Message": "Retry after the management controller is ready"},
				},
			},
		})
	}))
	defer server.Close()

	adapter := RedfishBMCAdapter{credentials: credentials, client: server.Client()}
	endpoint := models.BmcEndpoint{Endpoint: server.URL, Username: "admin", EncryptedPasswordRef: strconv.FormatUint(uint64(cred.ID), 10)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = adapter.Check(ctx, endpoint)
	if err == nil || !strings.Contains(err.Error(), "BMC is rebooting") || !strings.Contains(err.Error(), "Retry after") {
		t.Fatalf("expected Redfish error body in diagnostic, got %v", err)
	}
}

func TestRedfishClientTLSVerificationPolicy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	secureClient := redfishClient(false, "")
	secureRes, secureErr := secureClient.Get(server.URL)
	if secureErr == nil {
		secureRes.Body.Close()
		t.Fatalf("expected secure Redfish client to reject untrusted TLS certificate")
	}

	insecureClient := redfishClient(true, "")
	insecureRes, insecureErr := insecureClient.Get(server.URL)
	if insecureErr != nil {
		t.Fatalf("expected insecure Redfish client to allow lab self-signed TLS certificate: %v", insecureErr)
	}
	insecureRes.Body.Close()

	caPath := filepath.Join(t.TempDir(), "redfish-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write Redfish CA cert: %v", err)
	}
	caClient := redfishClient(false, caPath)
	caRes, caErr := caClient.Get(server.URL)
	if caErr != nil {
		t.Fatalf("expected Redfish client to trust configured CA certificate: %v", caErr)
	}
	caRes.Body.Close()
}

func TestIPMICommandAdapterUsesEnvPasswordAndHostPort(t *testing.T) {
	db := newCredentialTestDB(t)
	credentials := NewCredentialService(db, config.Config{CredentialKey: "ipmi-test-credential-key"})
	cred, err := credentials.Store("ipmi-password", "bmc", "admin", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store ipmi credential: %v", err)
	}

	var gotName string
	var gotArgs []string
	var helperCmd *exec.Cmd
	runner := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string{}, args...)
		helperCmd = exec.CommandContext(ctx, os.Args[0], "-test.run=TestIPMICommandAdapterHelperProcess", "--")
		helperCmd.Env = append(os.Environ(), "GO_WANT_IPMI_HELPER=1")
		return helperCmd
	}
	adapter := IPMICommandAdapter{credentials: credentials, runner: runner}
	endpoint := models.BmcEndpoint{Endpoint: "10.0.0.5:6623", Username: "admin", EncryptedPasswordRef: strconv.FormatUint(uint64(cred.ID), 10)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := adapter.PowerState(ctx, endpoint)
	if err != nil {
		t.Fatalf("ipmi power state: %v", err)
	}
	if state != "on" {
		t.Fatalf("expected ipmi state on, got %q", state)
	}
	expectedArgs := []string{"-I", "lanplus", "-H", "10.0.0.5", "-U", "admin", "-E", "-p", "6623", "power", "status"}
	if gotName != "ipmitool" || !reflect.DeepEqual(gotArgs, expectedArgs) {
		t.Fatalf("unexpected ipmitool command name=%q args=%v", gotName, gotArgs)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "secret-password") {
		t.Fatalf("ipmi password leaked into command arguments: %v", gotArgs)
	}
	if !containsEnv(helperCmd.Env, "IPMI_PASSWORD=secret-password") {
		t.Fatalf("expected IPMI_PASSWORD to be passed through environment, got %v", helperCmd.Env)
	}
}

func TestIPMICommandAdapterFirmwareInfoIncludesHardwareIDs(t *testing.T) {
	db := newCredentialTestDB(t)
	credentials := NewCredentialService(db, config.Config{CredentialKey: "ipmi-info-test-key"})
	cred, err := credentials.Store("ipmi-password", "bmc", "admin", "secret-password", "tester@example.com")
	if err != nil {
		t.Fatalf("store ipmi credential: %v", err)
	}

	runner := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helper := exec.CommandContext(ctx, os.Args[0], "-test.run=TestIPMICommandAdapterHelperProcess", "--")
		helper.Env = append(os.Environ(), "GO_WANT_IPMI_INFO_HELPER=1")
		return helper
	}
	adapter := IPMICommandAdapter{credentials: credentials, runner: runner}
	endpoint := models.BmcEndpoint{Endpoint: "10.0.0.5", Username: "admin", EncryptedPasswordRef: strconv.FormatUint(uint64(cred.ID), 10), Status: "ok"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := adapter.FirmwareInfo(ctx, endpoint)
	if err != nil {
		t.Fatalf("ipmi firmware info: %v", err)
	}
	if info.DeviceID != "32" || info.DeviceRevision != "1" || info.ManufacturerID != "674" || info.ProductID != "256" {
		t.Fatalf("expected IPMI hardware IDs, got %#v", info)
	}
	if info.Manufacturer != "Dell Inc." || info.Model != "PowerEdge R650" || info.BMCVersion != "7.10" {
		t.Fatalf("expected IPMI identity fields, got %#v", info)
	}
}

func TestBMCServiceRejectsEndpointTypeAdapterMismatch(t *testing.T) {
	db := newCredentialTestDB(t)
	if err := db.AutoMigrate(&models.BmcEndpoint{}); err != nil {
		t.Fatalf("migrate bmc endpoint: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: 1, Type: "ipmi", Protocol: "ipmi", Endpoint: "10.0.0.5", Username: "admin", Status: "ok"}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	service := NewBMCService(db, config.Config{CredentialKey: "bmc-mismatch-test-key", BMCAdapter: "redfish"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	checked, err := service.Check(ctx, "1")
	if err == nil || !strings.Contains(err.Error(), "does not match BMC_ADAPTER=redfish") {
		t.Fatalf("expected adapter mismatch error, endpoint=%#v err=%v", checked, err)
	}
	if checked.Status != "error" || checked.LastCheckedAt == nil {
		t.Fatalf("expected mismatch check to mark endpoint error: %#v", checked)
	}
	_, info, err := service.FirmwareInfo(ctx, "1")
	if err == nil || info.Adapter != "redfish" {
		t.Fatalf("expected firmware probe to reject mismatched adapter with adapter context, info=%#v err=%v", info, err)
	}
}

func TestBMCServiceRejectsProductionPlainHTTPRedfishEndpoint(t *testing.T) {
	db := newCredentialTestDB(t)
	if err := db.AutoMigrate(&models.BmcEndpoint{}); err != nil {
		t.Fatalf("migrate bmc endpoint: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: 1, Type: "redfish", Protocol: "http", Endpoint: "http://bmc.example.test", Username: "admin", Status: "ok"}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	service := NewBMCService(db, config.Config{AppEnv: "production", CredentialKey: "bmc-production-redfish-key", BMCAdapter: "redfish", RedfishInsecureTLS: false})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	checked, err := service.Check(ctx, "1")
	if err == nil || !strings.Contains(err.Error(), "production Redfish endpoint must use https") {
		t.Fatalf("expected production HTTP Redfish endpoint error, endpoint=%#v err=%v", checked, err)
	}
	if checked.Status != "error" || checked.LastCheckedAt == nil {
		t.Fatalf("expected production HTTP Redfish check to mark endpoint error: %#v", checked)
	}
	_, info, err := service.FirmwareInfo(ctx, "1")
	if err == nil || !strings.Contains(err.Error(), "production Redfish endpoint must use https") || info.Adapter != "redfish" {
		t.Fatalf("expected firmware probe to reject production HTTP Redfish endpoint, info=%#v err=%v", info, err)
	}
}

func TestBMCServiceRejectsInvalidIPMIEndpointBeforeCommand(t *testing.T) {
	db := newCredentialTestDB(t)
	if err := db.AutoMigrate(&models.BmcEndpoint{}); err != nil {
		t.Fatalf("migrate bmc endpoint: %v", err)
	}
	endpoint := models.BmcEndpoint{ServerID: 1, Type: "ipmi", Protocol: "ipmi", Endpoint: "10.0.0.5:notaport", Username: "admin", Status: "ok"}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatalf("create bmc endpoint: %v", err)
	}
	service := NewBMCService(db, config.Config{CredentialKey: "bmc-invalid-ipmi-key", BMCAdapter: "ipmi"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	checked, err := service.Check(ctx, "1")
	if err == nil || !strings.Contains(err.Error(), "IPMI endpoint port") {
		t.Fatalf("expected invalid IPMI port error before command execution, endpoint=%#v err=%v", checked, err)
	}
	if checked.Status != "error" || checked.LastCheckedAt == nil {
		t.Fatalf("expected invalid IPMI check to mark endpoint error: %#v", checked)
	}
}

func TestIPMICommandAdapterHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_IPMI_INFO_HELPER") == "1" {
		if os.Getenv("IPMI_PASSWORD") == "" {
			_, _ = fmt.Fprintln(os.Stderr, "missing IPMI_PASSWORD")
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(os.Stdout, "Device ID                 : 32")
		_, _ = fmt.Fprintln(os.Stdout, "Device Revision           : 1")
		_, _ = fmt.Fprintln(os.Stdout, "Firmware Revision         : 7.10")
		_, _ = fmt.Fprintln(os.Stdout, "Manufacturer ID           : 674")
		_, _ = fmt.Fprintln(os.Stdout, "Manufacturer Name         : Dell Inc.")
		_, _ = fmt.Fprintln(os.Stdout, "Product ID                : 256")
		_, _ = fmt.Fprintln(os.Stdout, "Product Name              : PowerEdge R650")
		os.Exit(0)
	}
	if os.Getenv("GO_WANT_IPMI_HELPER") != "1" {
		return
	}
	if os.Getenv("IPMI_PASSWORD") == "" {
		_, _ = fmt.Fprintln(os.Stderr, "missing IPMI_PASSWORD")
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, "Chassis Power is on")
	os.Exit(0)
}

func containsEnv(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
