package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestIPMICommandAdapterHelperProcess(t *testing.T) {
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
