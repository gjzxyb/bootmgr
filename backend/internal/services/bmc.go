package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type BMCAdapter interface {
	Name() string
	PowerState(ctx context.Context, endpoint models.BmcEndpoint) (string, error)
	SetPowerState(ctx context.Context, endpoint models.BmcEndpoint, state string) (string, error)
	Check(ctx context.Context, endpoint models.BmcEndpoint) error
	FirmwareInfo(ctx context.Context, endpoint models.BmcEndpoint) (BMCFirmwareInfo, error)
}

type BMCFirmwareInfo struct {
	Adapter         string     `json:"adapter"`
	EndpointStatus  string     `json:"endpoint_status"`
	Manufacturer    string     `json:"manufacturer,omitempty"`
	Model           string     `json:"model,omitempty"`
	SerialNumber    string     `json:"serial_number,omitempty"`
	FirmwareVersion string     `json:"firmware_version,omitempty"`
	BIOSVersion     string     `json:"bios_version,omitempty"`
	BMCVersion      string     `json:"bmc_version,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
}

type BMCService struct {
	db      *gorm.DB
	adapter BMCAdapter
}

func NewBMCService(db *gorm.DB, cfg config.Config) BMCService {
	creds := NewCredentialService(db, cfg)
	adapter := BMCAdapter(SimulatedBMCAdapter{})
	switch strings.ToLower(strings.TrimSpace(cfg.BMCAdapter)) {
	case "redfish":
		adapter = RedfishBMCAdapter{credentials: creds, client: redfishClient()}
	case "ipmi":
		adapter = IPMICommandAdapter{credentials: creds, runner: exec.CommandContext}
	}
	return BMCService{db: db, adapter: adapter}
}

func (s BMCService) AdapterName() string { return s.adapter.Name() }

func (s BMCService) Endpoint(serverID string) (models.BmcEndpoint, error) {
	var endpoint models.BmcEndpoint
	err := s.db.Where("server_id = ?", serverID).First(&endpoint).Error
	return endpoint, err
}

func (s BMCService) PowerState(ctx context.Context, serverID string) (models.BmcEndpoint, string, error) {
	endpoint, err := s.Endpoint(serverID)
	if err != nil {
		return endpoint, "", err
	}
	state, err := s.adapter.PowerState(ctx, endpoint)
	return endpoint, state, err
}

func (s BMCService) SetPowerState(ctx context.Context, serverID string, state string) (models.BmcEndpoint, string, error) {
	endpoint, err := s.Endpoint(serverID)
	if err != nil {
		return endpoint, "", err
	}
	actual, err := s.adapter.SetPowerState(ctx, endpoint, state)
	if err != nil {
		return endpoint, "", err
	}
	s.db.Model(&endpoint).Update("power_state", actual)
	endpoint.PowerState = actual
	return endpoint, actual, nil
}

func (s BMCService) Check(ctx context.Context, serverID string) (models.BmcEndpoint, error) {
	endpoint, err := s.Endpoint(serverID)
	if err != nil {
		return endpoint, err
	}
	if err := s.adapter.Check(ctx, endpoint); err != nil {
		now := time.Now().UTC()
		s.db.Model(&endpoint).Updates(map[string]any{"status": "error", "last_checked_at": now})
		endpoint.Status = "error"
		endpoint.LastCheckedAt = &now
		return endpoint, err
	}
	now := time.Now().UTC()
	s.db.Model(&endpoint).Updates(map[string]any{"status": "ok", "last_checked_at": now})
	endpoint.Status = "ok"
	endpoint.LastCheckedAt = &now
	return endpoint, nil
}

func (s BMCService) FirmwareInfo(ctx context.Context, serverID string) (models.BmcEndpoint, BMCFirmwareInfo, error) {
	endpoint, err := s.Endpoint(serverID)
	if err != nil {
		return endpoint, BMCFirmwareInfo{}, err
	}
	info, err := s.adapter.FirmwareInfo(ctx, endpoint)
	if info.Adapter == "" {
		info.Adapter = s.adapter.Name()
	}
	if info.EndpointStatus == "" {
		info.EndpointStatus = endpoint.Status
	}
	if info.LastCheckedAt == nil {
		info.LastCheckedAt = endpoint.LastCheckedAt
	}
	return endpoint, info, err
}

type SimulatedBMCAdapter struct{}

func (SimulatedBMCAdapter) Name() string { return "simulated" }
func (SimulatedBMCAdapter) PowerState(ctx context.Context, endpoint models.BmcEndpoint) (string, error) {
	return endpoint.PowerState, nil
}
func (SimulatedBMCAdapter) SetPowerState(ctx context.Context, endpoint models.BmcEndpoint, state string) (string, error) {
	if state == "reboot" {
		return "on", nil
	}
	if state != "on" && state != "off" {
		return "", errors.New("unsupported power state")
	}
	return state, nil
}
func (SimulatedBMCAdapter) Check(ctx context.Context, endpoint models.BmcEndpoint) error { return nil }
func (SimulatedBMCAdapter) FirmwareInfo(ctx context.Context, endpoint models.BmcEndpoint) (BMCFirmwareInfo, error) {
	return BMCFirmwareInfo{
		Adapter:         "simulated",
		EndpointStatus:  endpoint.Status,
		Manufacturer:    "Simulated",
		Model:           "Virtual BMC",
		SerialNumber:    fmt.Sprintf("SIM-%06d", endpoint.ServerID),
		FirmwareVersion: "sim-1.0.0",
		BIOSVersion:     "sim-bios-1.0.0",
		BMCVersion:      "sim-bmc-1.0.0",
		LastCheckedAt:   endpoint.LastCheckedAt,
	}, nil
}

type RedfishBMCAdapter struct {
	credentials CredentialService
	client      *http.Client
}

func (RedfishBMCAdapter) Name() string { return "redfish" }
func (a RedfishBMCAdapter) SetPowerState(ctx context.Context, endpoint models.BmcEndpoint, state string) (string, error) {
	resetType := map[string]string{"on": "On", "off": "ForceOff", "reboot": "ForceRestart"}[state]
	if resetType == "" {
		return "", errors.New("unsupported power state")
	}
	system, _, err := a.redfishSystem(ctx, endpoint)
	if err != nil {
		return "", err
	}
	resetTarget := system.Actions.ComputerSystemReset.Target
	if resetTarget == "" {
		resetTarget = strings.TrimRight(system.ODataID, "/") + "/Actions/ComputerSystem.Reset"
	}
	if resetTarget == "" {
		resetTarget = "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"
	}
	if err := a.redfishPost(ctx, endpoint, resetTarget, map[string]string{"ResetType": resetType}); err != nil {
		return "", err
	}
	if state == "reboot" {
		return "on", nil
	}
	return state, nil
}

type commandRunner func(context.Context, string, ...string) *exec.Cmd

type IPMICommandAdapter struct {
	credentials CredentialService
	runner      commandRunner
}

func (IPMICommandAdapter) Name() string { return "ipmi" }

func (a IPMICommandAdapter) PowerState(ctx context.Context, endpoint models.BmcEndpoint) (string, error) {
	out, err := a.run(ctx, endpoint, "power", "status")
	if err != nil {
		return "", err
	}
	text := strings.ToLower(string(out))
	switch {
	case strings.Contains(text, "on"):
		return "on", nil
	case strings.Contains(text, "off"):
		return "off", nil
	default:
		return strings.TrimSpace(text), nil
	}
}

func (a IPMICommandAdapter) SetPowerState(ctx context.Context, endpoint models.BmcEndpoint, state string) (string, error) {
	action := map[string]string{"on": "on", "off": "off", "reboot": "reset"}[state]
	if action == "" {
		return "", errors.New("unsupported power state")
	}
	if _, err := a.run(ctx, endpoint, "power", action); err != nil {
		return "", err
	}
	if state == "reboot" {
		return "on", nil
	}
	return state, nil
}

func (a IPMICommandAdapter) Check(ctx context.Context, endpoint models.BmcEndpoint) error {
	_, err := a.run(ctx, endpoint, "chassis", "status")
	return err
}

func (a IPMICommandAdapter) FirmwareInfo(ctx context.Context, endpoint models.BmcEndpoint) (BMCFirmwareInfo, error) {
	out, err := a.run(ctx, endpoint, "mc", "info")
	info := BMCFirmwareInfo{
		Adapter:        a.Name(),
		EndpointStatus: endpoint.Status,
		LastCheckedAt:  endpoint.LastCheckedAt,
	}
	if err != nil {
		return info, err
	}
	text := string(out)
	info.Manufacturer = ipmiInfoField(text, "Manufacturer Name")
	info.Model = ipmiInfoField(text, "Product Name")
	info.BMCVersion = ipmiInfoField(text, "Firmware Revision")
	info.FirmwareVersion = info.BMCVersion
	return info, nil
}

func (a IPMICommandAdapter) run(ctx context.Context, endpoint models.BmcEndpoint, args ...string) ([]byte, error) {
	if endpoint.EncryptedPasswordRef == "" {
		return nil, errors.New("missing credential reference")
	}
	credID, err := parseUint(endpoint.EncryptedPasswordRef)
	if err != nil {
		return nil, err
	}
	secret, err := a.credentials.Secret(credID)
	if err != nil {
		return nil, err
	}
	host, port := ipmiHostPort(endpoint.Endpoint)
	if host == "" {
		return nil, errors.New("missing IPMI endpoint host")
	}
	base := []string{"-I", "lanplus", "-H", host, "-U", endpoint.Username, "-E"}
	if port != "" {
		base = append(base, "-p", port)
	}
	base = append(base, args...)
	runner := a.runner
	if runner == nil {
		runner = exec.CommandContext
	}
	cmd := runner(ctx, "ipmitool", base...)
	cmd.Env = append(cmd.Environ(), "IPMI_PASSWORD="+secret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("ipmitool %s failed: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
func (a RedfishBMCAdapter) Check(ctx context.Context, endpoint models.BmcEndpoint) error {
	_, err := a.redfishGet(ctx, endpoint, "/redfish/v1")
	return err
}

func (a RedfishBMCAdapter) PowerState(ctx context.Context, endpoint models.BmcEndpoint) (string, error) {
	system, _, err := a.redfishSystem(ctx, endpoint)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(system.PowerState) {
	case "on":
		return "on", nil
	case "off":
		return "off", nil
	default:
		return strings.ToLower(system.PowerState), nil
	}
}

func (a RedfishBMCAdapter) FirmwareInfo(ctx context.Context, endpoint models.BmcEndpoint) (BMCFirmwareInfo, error) {
	info := BMCFirmwareInfo{
		Adapter:        a.Name(),
		EndpointStatus: endpoint.Status,
		LastCheckedAt:  endpoint.LastCheckedAt,
	}
	system, _, err := a.redfishSystem(ctx, endpoint)
	if err != nil {
		return info, err
	}
	info.Manufacturer = system.Manufacturer
	info.Model = system.Model
	info.SerialNumber = system.SerialNumber
	info.BIOSVersion = system.BIOSVersion
	info.FirmwareVersion = system.FirmwareVersion

	managerPath, _ := a.firstCollectionMember(ctx, endpoint, "/redfish/v1/Managers")
	if managerPath == "" {
		managerPath = "/redfish/v1/Managers/1"
	}
	managerBody, err := a.redfishGet(ctx, endpoint, managerPath)
	if err == nil {
		var manager struct {
			Manufacturer    string `json:"Manufacturer"`
			Model           string `json:"Model"`
			SerialNumber    string `json:"SerialNumber"`
			FirmwareVersion string `json:"FirmwareVersion"`
		}
		if err := json.Unmarshal(managerBody, &manager); err != nil {
			return info, err
		}
		if info.Manufacturer == "" {
			info.Manufacturer = manager.Manufacturer
		}
		if info.Model == "" {
			info.Model = manager.Model
		}
		if info.SerialNumber == "" {
			info.SerialNumber = manager.SerialNumber
		}
		info.BMCVersion = manager.FirmwareVersion
		if info.FirmwareVersion == "" {
			info.FirmwareVersion = manager.FirmwareVersion
		}
	}
	return info, nil
}

func (a RedfishBMCAdapter) redfishGet(ctx context.Context, endpoint models.BmcEndpoint, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redfishURL(endpoint.Endpoint, path), nil)
	if err != nil {
		return nil, err
	}
	if err := a.authorize(req, endpoint); err != nil {
		return nil, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("redfish GET %s returned %d", path, res.StatusCode)
	}
	var buf bytes.Buffer
	_, err = buf.ReadFrom(res.Body)
	return buf.Bytes(), err
}

type redfishSystemResource struct {
	ODataID         string `json:"@odata.id"`
	PowerState      string `json:"PowerState"`
	Manufacturer    string `json:"Manufacturer"`
	Model           string `json:"Model"`
	SerialNumber    string `json:"SerialNumber"`
	BIOSVersion     string `json:"BiosVersion"`
	FirmwareVersion string `json:"FirmwareVersion"`
	Actions         struct {
		ComputerSystemReset struct {
			Target string `json:"target"`
		} `json:"#ComputerSystem.Reset"`
	} `json:"Actions"`
}

func (a RedfishBMCAdapter) redfishSystem(ctx context.Context, endpoint models.BmcEndpoint) (redfishSystemResource, string, error) {
	path, err := a.firstCollectionMember(ctx, endpoint, "/redfish/v1/Systems")
	if err != nil || path == "" {
		path = "/redfish/v1/Systems/1"
	}
	body, err := a.redfishGet(ctx, endpoint, path)
	if err != nil {
		return redfishSystemResource{}, path, err
	}
	var system redfishSystemResource
	if err := json.Unmarshal(body, &system); err != nil {
		return redfishSystemResource{}, path, err
	}
	if system.ODataID == "" {
		system.ODataID = path
	}
	return system, path, nil
}

func (a RedfishBMCAdapter) firstCollectionMember(ctx context.Context, endpoint models.BmcEndpoint, path string) (string, error) {
	body, err := a.redfishGet(ctx, endpoint, path)
	if err != nil {
		return "", err
	}
	var collection struct {
		Members []struct {
			ODataID string `json:"@odata.id"`
		} `json:"Members"`
	}
	if err := json.Unmarshal(body, &collection); err != nil {
		return "", err
	}
	if len(collection.Members) == 0 {
		return "", nil
	}
	return collection.Members[0].ODataID, nil
}

func (a RedfishBMCAdapter) redfishPost(ctx context.Context, endpoint models.BmcEndpoint, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, redfishURL(endpoint.Endpoint, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := a.authorize(req, endpoint); err != nil {
		return err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("redfish POST %s returned %d", path, res.StatusCode)
	}
	return nil
}

func (a RedfishBMCAdapter) authorize(req *http.Request, endpoint models.BmcEndpoint) error {
	if endpoint.EncryptedPasswordRef == "" {
		return errors.New("missing credential reference")
	}
	credID, err := parseUint(endpoint.EncryptedPasswordRef)
	if err != nil {
		return err
	}
	secret, err := a.credentials.Secret(credID)
	if err != nil {
		return err
	}
	req.SetBasicAuth(endpoint.Username, secret)
	return nil
}

func redfishURL(endpoint, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(endpoint, "/") + path
}
func ipmiHost(endpoint string) string {
	host, _ := ipmiHostPort(endpoint)
	return host
}
func ipmiHostPort(endpoint string) (string, string) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", ""
	}
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		host, port, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr == nil {
			return host, port
		}
		return parsed.Hostname(), parsed.Port()
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err == nil {
		return host, port
	}
	return endpoint, ""
}
func ipmiInfoField(text, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, line := range strings.Split(text, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.ToLower(strings.TrimSpace(name)) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func redfishClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}
func parseUint(text string) (uint, error) {
	var v uint
	_, err := fmt.Sscanf(text, "%d", &v)
	return v, err
}
