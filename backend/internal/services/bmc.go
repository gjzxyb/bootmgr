package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"strconv"
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
	Stage           string     `json:"stage,omitempty"`
	Manufacturer    string     `json:"manufacturer,omitempty"`
	ManufacturerID  string     `json:"manufacturer_id,omitempty"`
	Model           string     `json:"model,omitempty"`
	ProductID       string     `json:"product_id,omitempty"`
	DeviceID        string     `json:"device_id,omitempty"`
	DeviceRevision  string     `json:"device_revision,omitempty"`
	SerialNumber    string     `json:"serial_number,omitempty"`
	FirmwareVersion string     `json:"firmware_version,omitempty"`
	BIOSVersion     string     `json:"bios_version,omitempty"`
	BMCVersion      string     `json:"bmc_version,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
}

type BMCService struct {
	db      *gorm.DB
	cfg     config.Config
	adapter BMCAdapter
}

func NewBMCService(db *gorm.DB, cfg config.Config) BMCService {
	creds := NewCredentialService(db, cfg)
	adapter := BMCAdapter(SimulatedBMCAdapter{})
	switch strings.ToLower(strings.TrimSpace(cfg.BMCAdapter)) {
	case "redfish":
		adapter = RedfishBMCAdapter{credentials: creds, client: redfishClient(cfg.RedfishInsecureTLS, cfg.RedfishCACertPath)}
	case "ipmi":
		adapter = IPMICommandAdapter{credentials: creds, runner: exec.CommandContext}
	}
	return BMCService{db: db, cfg: cfg, adapter: adapter}
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
	if err := s.ensureEndpointMatchesAdapter(endpoint); err != nil {
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
	if err := s.ensureEndpointMatchesAdapter(endpoint); err != nil {
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
	if err := s.ensureEndpointMatchesAdapter(endpoint); err != nil {
		now := time.Now().UTC()
		s.db.Model(&endpoint).Updates(map[string]any{"status": "error", "last_checked_at": now})
		endpoint.Status = "error"
		endpoint.LastCheckedAt = &now
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
	if err := s.ensureEndpointMatchesAdapter(endpoint); err != nil {
		return endpoint, BMCFirmwareInfo{Adapter: s.adapter.Name(), EndpointStatus: endpoint.Status, LastCheckedAt: endpoint.LastCheckedAt}, err
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

func (s BMCService) ensureEndpointMatchesAdapter(endpoint models.BmcEndpoint) error {
	adapter := strings.ToLower(strings.TrimSpace(s.adapter.Name()))
	if adapter == "simulated" {
		return nil
	}
	endpointType := strings.ToLower(strings.TrimSpace(endpoint.Type))
	if endpointType != adapter {
		return fmt.Errorf("BMC endpoint type %q does not match BMC_ADAPTER=%s", endpoint.Type, adapter)
	}
	if adapter == "redfish" {
		parsed, err := url.Parse(strings.TrimSpace(endpoint.Endpoint))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("Redfish endpoint must be an http or https URL")
		}
		protocol := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
		if protocol != "" && protocol != parsed.Scheme {
			return errors.New("Redfish protocol must match endpoint URL scheme")
		}
		if config.IsProduction(s.cfg.AppEnv) && parsed.Scheme != "https" {
			return errors.New("production Redfish endpoint must use https")
		}
	}
	if adapter == "ipmi" {
		host, port := ipmiHostPort(endpoint.Endpoint)
		if strings.TrimSpace(host) == "" {
			return errors.New("IPMI endpoint host is required")
		}
		if !validIPMIHost(host) {
			return errors.New("IPMI endpoint host is invalid")
		}
		if port != "" {
			parsedPort, err := strconv.Atoi(port)
			if err != nil || parsedPort < 1 || parsedPort > 65535 {
				return errors.New("IPMI endpoint port must be between 1 and 65535")
			}
		}
		if strings.TrimSpace(endpoint.Username) == "" {
			return errors.New("IPMI username is required")
		}
	}
	return nil
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
	if resetTarget != "" {
		resetTarget = redfishResolveResourceChildLink(system.ODataID, resetTarget)
	} else if system.ODataID != "" {
		resetTarget = strings.TrimRight(redfishNormalizeServiceRootLink(system.ODataID), "/") + "/Actions/ComputerSystem.Reset"
	} else {
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
	info.DeviceID = ipmiInfoField(text, "Device ID")
	info.DeviceRevision = ipmiInfoField(text, "Device Revision")
	info.ManufacturerID = ipmiInfoField(text, "Manufacturer ID")
	info.Manufacturer = ipmiInfoField(text, "Manufacturer Name")
	info.ProductID = ipmiInfoField(text, "Product ID")
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
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, redfishHTTPError(http.MethodGet, path, res.StatusCode, body)
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
	return redfishNormalizeServiceRootLink(collection.Members[0].ODataID), nil
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
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return redfishHTTPError(http.MethodPost, path, res.StatusCode, body)
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
	if parsed, err := url.Parse(strings.TrimSpace(endpoint)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		basePath := strings.TrimRight(parsed.EscapedPath(), "/")
		if strings.EqualFold(basePath, "/redfish/v1") && strings.HasPrefix(strings.ToLower(path), "/redfish/v1") {
			parsed.Path = path
			parsed.RawPath = ""
			return parsed.String()
		}
	}
	return strings.TrimRight(endpoint, "/") + path
}

func redfishNormalizeServiceRootLink(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	value = strings.TrimPrefix(value, "./")
	if strings.HasPrefix(value, "/") {
		return value
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "redfish/v1"):
		return "/" + value
	case strings.HasPrefix(lower, "v1/"):
		return "/redfish/" + value
	default:
		return "/redfish/v1/" + strings.TrimLeft(value, "/")
	}
}

func redfishResolveResourceChildLink(basePath, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "/") ||
		strings.HasPrefix(lower, "redfish/v1") ||
		strings.HasPrefix(lower, "v1/") {
		return redfishNormalizeServiceRootLink(value)
	}
	base := redfishNormalizeServiceRootLink(basePath)
	if base == "" {
		base = "/redfish/v1"
	}
	if strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	if strings.HasPrefix(value, "../") {
		return pathpkg.Clean(pathpkg.Dir(base) + "/" + value)
	}
	if strings.HasPrefix(strings.ToLower(value), "actions/") {
		return pathpkg.Clean(strings.TrimRight(base, "/") + "/" + value)
	}
	return redfishNormalizeServiceRootLink(value)
}

func redfishHTTPError(method, target string, status int, body []byte) error {
	if summary := redfishErrorSummary(body); summary != "" {
		return fmt.Errorf("redfish %s %s returned %d: %s", method, target, status, summary)
	}
	return fmt.Errorf("redfish %s %s returned %d", method, target, status)
}

func redfishErrorSummary(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Error struct {
			Code         string `json:"code"`
			Message      string `json:"message"`
			ExtendedInfo []struct {
				MessageID string `json:"MessageId"`
				Message   string `json:"Message"`
			} `json:"@Message.ExtendedInfo"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		parts := []string{}
		if payload.Error.Code != "" {
			parts = append(parts, payload.Error.Code)
		}
		if payload.Error.Message != "" {
			parts = append(parts, payload.Error.Message)
		}
		for _, item := range payload.Error.ExtendedInfo {
			switch {
			case item.Message != "":
				parts = append(parts, item.Message)
			case item.MessageID != "":
				parts = append(parts, item.MessageID)
			}
			if len(parts) >= 3 {
				break
			}
		}
		if len(parts) > 0 {
			return truncateDiagnostic(strings.Join(parts, ": "), 512)
		}
	}
	return truncateDiagnostic(strings.Join(strings.Fields(string(body)), " "), 512)
}

func truncateDiagnostic(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
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
func validIPMIHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0 && i < len(label)-1) {
				continue
			}
			return false
		}
	}
	return true
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
func redfishClient(insecureTLS bool, caCertPath string) *http.Client {
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if insecureTLS {
		transport.TLSClientConfig.InsecureSkipVerify = true
	} else if strings.TrimSpace(caCertPath) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if data, err := os.ReadFile(caCertPath); err == nil && pool.AppendCertsFromPEM(data) {
			transport.TLSClientConfig.RootCAs = pool
		}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport}
}
func parseUint(text string) (uint, error) {
	var v uint
	_, err := fmt.Sscanf(text, "%d", &v)
	return v, err
}
