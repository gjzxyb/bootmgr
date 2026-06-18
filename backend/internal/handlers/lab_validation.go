package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
)

type labValidationCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type labValidationEnvironment struct {
	AppEnv            string `json:"app_env"`
	BootBaseURL       string `json:"boot_base_url"`
	BMCAdapter        string `json:"bmc_adapter"`
	CollectorMode     string `json:"collector_mode"`
	SSHOperationsMode string `json:"ssh_operations_mode"`
}

type labValidationPXE struct {
	Enabled            bool           `json:"enabled"`
	Mode               string         `json:"mode"`
	BindInterface      string         `json:"bind_interface"`
	DHCPListenAddr     string         `json:"dhcp_listen_addr"`
	DHCPServerIP       string         `json:"dhcp_server_ip"`
	DHCPLeaseStart     string         `json:"dhcp_lease_start"`
	DHCPLeaseEnd       string         `json:"dhcp_lease_end"`
	TFTPListenAddr     string         `json:"tftp_listen_addr"`
	TFTPRoot           string         `json:"tftp_root"`
	BootfileUEFI       string         `json:"bootfile_uefi"`
	BootfileBIOS       string         `json:"bootfile_bios"`
	DeploymentNetworks int64          `json:"deployment_networks"`
	BootEvents         int64          `json:"boot_events"`
	RecentBootEvents   []labBootEvent `json:"recent_boot_events"`
	RuntimeIssues      []config.Issue `json:"runtime_issues"`
}

type labBootEvent struct {
	ID           uint      `json:"id"`
	MAC          string    `json:"mac"`
	Architecture string    `json:"architecture"`
	Firmware     string    `json:"firmware"`
	RemoteAddr   string    `json:"remote_addr"`
	ServerID     *uint     `json:"server_id,omitempty"`
	DeploymentID *uint     `json:"deployment_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type labValidationBMC struct {
	Adapter         string      `json:"adapter"`
	Total           int64       `json:"total"`
	OK              int64       `json:"ok"`
	Error           int64       `json:"error"`
	Unknown         int64       `json:"unknown"`
	LastCheckedAt   *time.Time  `json:"last_checked_at,omitempty"`
	RecentEndpoints []labBMCRef `json:"recent_endpoints"`
}

type labBMCRef struct {
	ServerID      uint       `json:"server_id"`
	Hostname      string     `json:"hostname"`
	AssetNo       string     `json:"asset_no"`
	Type          string     `json:"type"`
	Protocol      string     `json:"protocol"`
	Endpoint      string     `json:"endpoint"`
	Status        string     `json:"status"`
	PowerState    string     `json:"power_state"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type labValidationSSH struct {
	CollectorMode     string      `json:"collector_mode"`
	OperationsMode    string      `json:"operations_mode"`
	Total             int64       `json:"total"`
	OK                int64       `json:"ok"`
	Error             int64       `json:"error"`
	Configured        int64       `json:"configured"`
	Unknown           int64       `json:"unknown"`
	LastCheckedAt     *time.Time  `json:"last_checked_at,omitempty"`
	RecentSSHAccesses []labSSHRef `json:"recent_ssh_accesses"`
}

type labSSHRef struct {
	ServerID      uint       `json:"server_id"`
	Hostname      string     `json:"hostname"`
	AssetNo       string     `json:"asset_no"`
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	Username      string     `json:"username"`
	AuthType      string     `json:"auth_type"`
	Status        string     `json:"status"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type labValidationRunResult struct {
	Kind      string     `json:"kind"`
	ServerID  uint       `json:"server_id"`
	Hostname  string     `json:"hostname"`
	AssetNo   string     `json:"asset_no"`
	Status    string     `json:"status"`
	Message   string     `json:"message"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type labValidationReport struct {
	Status      string                   `json:"status"`
	GeneratedAt time.Time                `json:"generated_at"`
	Environment labValidationEnvironment `json:"environment"`
	Checks      []labValidationCheck     `json:"checks"`
	PXE         labValidationPXE         `json:"pxe"`
	BMC         labValidationBMC         `json:"bmc"`
	SSH         labValidationSSH         `json:"ssh"`
	RunResults  []labValidationRunResult `json:"run_results,omitempty"`
}

func (h Handler) getLabValidation(c *gin.Context) {
	c.JSON(http.StatusOK, h.buildLabValidationReport(nil))
}

func (h Handler) runLabValidation(c *gin.Context) {
	var req struct {
		CheckBMC *bool `json:"check_bmc"`
		CheckSSH *bool `json:"check_ssh"`
		CheckPXE *bool `json:"check_pxe"`
		Limit    int   `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	runBMC := req.CheckBMC == nil || *req.CheckBMC
	runSSH := req.CheckSSH == nil || *req.CheckSSH
	runPXE := req.CheckPXE == nil || *req.CheckPXE

	results := []labValidationRunResult{}
	if runPXE {
		results = append(results, h.runLabPXEChecks(c.Request.Context())...)
	}
	if runBMC {
		results = append(results, h.runLabBMCChecks(c.Request.Context(), limit)...)
	}
	if runSSH {
		results = append(results, h.runLabSSHChecks(c.Request.Context(), limit)...)
	}
	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "system.lab_validation.run", "system", "lab-validation", "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, h.buildLabValidationReport(results))
}

func (h Handler) buildLabValidationReport(runResults []labValidationRunResult) labValidationReport {
	report := labValidationReport{
		Status:      "ok",
		GeneratedAt: time.Now().UTC(),
		Environment: labValidationEnvironment{
			AppEnv:            h.cfg.AppEnv,
			BootBaseURL:       h.cfg.BootBaseURL,
			BMCAdapter:        h.bmc.AdapterName(),
			CollectorMode:     h.cfg.CollectorMode,
			SSHOperationsMode: h.cfg.SSHOperationsMode,
		},
		Checks:     []labValidationCheck{},
		RunResults: runResults,
	}
	h.fillPXELabValidation(&report)
	h.fillBMCLabValidation(&report)
	h.fillSSHLabValidation(&report)
	report.addPXERunChecks(runResults)
	return report
}

func (r *labValidationReport) addCheck(name string, status string, message string) {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "warning"
	}
	if status == "error" {
		r.Status = "error"
	} else if status == "warning" && r.Status == "ok" {
		r.Status = "warning"
	}
	r.Checks = append(r.Checks, labValidationCheck{Name: name, Status: status, Message: message})
}

func (r *labValidationReport) addPXERunChecks(results []labValidationRunResult) {
	for _, result := range results {
		if result.Kind != "pxe_http" && result.Kind != "pxe_dhcp" && result.Kind != "pxe_tftp" {
			continue
		}
		status := "ok"
		if result.Status == "failed" {
			status = "error"
		} else if result.Status == "skipped" {
			status = "warning"
		}
		r.addCheck(result.Kind, status, result.Message)
	}
}

func (h Handler) fillPXELabValidation(report *labValidationReport) {
	pxe := labValidationPXE{
		Enabled:        h.cfg.BootServicesEnabled,
		Mode:           h.cfg.BootServiceMode,
		BindInterface:  h.cfg.BootBindInterface,
		DHCPListenAddr: h.cfg.BootDHCPListenAddr,
		DHCPServerIP:   h.cfg.BootDHCPServerIP,
		DHCPLeaseStart: h.cfg.BootDHCPLeaseStart,
		DHCPLeaseEnd:   h.cfg.BootDHCPLeaseEnd,
		TFTPListenAddr: h.cfg.BootTFTPListenAddr,
		TFTPRoot:       h.cfg.BootTFTPRoot,
		BootfileUEFI:   h.cfg.BootTFTPBootfileUEFI,
		BootfileBIOS:   h.cfg.BootTFTPBootfileBIOS,
		RuntimeIssues:  config.BootRuntimeIssues(h.cfg),
	}
	h.db.Model(&models.NetworkConfig{}).Where("purpose = ? AND status = ?", "deployment", "enabled").Count(&pxe.DeploymentNetworks)
	h.db.Model(&models.BootEvent{}).Count(&pxe.BootEvents)
	var events []models.BootEvent
	h.db.Order("created_at desc, id desc").Limit(10).Find(&events)
	pxe.RecentBootEvents = make([]labBootEvent, 0, len(events))
	for _, event := range events {
		pxe.RecentBootEvents = append(pxe.RecentBootEvents, labBootEvent{
			ID: event.ID, MAC: event.MAC, Architecture: event.Architecture, Firmware: event.Firmware,
			RemoteAddr: event.RemoteAddr, ServerID: event.ServerID, DeploymentID: event.DeploymentID, CreatedAt: event.CreatedAt,
		})
	}
	report.PXE = pxe

	if !pxe.Enabled {
		report.addCheck("pxe_services", "warning", "PXE/DHCP/TFTP listeners are disabled")
	} else {
		status := "ok"
		message := "PXE/DHCP/TFTP runtime checks passed"
		for _, issue := range pxe.RuntimeIssues {
			if issue.Level == "error" {
				status = "error"
				message = issue.Message
				break
			}
			if issue.Level == "warning" && status == "ok" {
				status = "warning"
				message = issue.Message
			}
		}
		report.addCheck("pxe_services", status, message)
	}
	if pxe.DeploymentNetworks == 0 {
		report.addCheck("deployment_network", "warning", "no enabled deployment network is configured")
	} else {
		report.addCheck("deployment_network", "ok", fmt.Sprintf("%d enabled deployment network(s)", pxe.DeploymentNetworks))
	}
	if pxe.BootEvents == 0 {
		report.addCheck("pxe_boot_events", "warning", "no PXE boot event has been recorded yet")
	} else {
		report.addCheck("pxe_boot_events", "ok", fmt.Sprintf("%d PXE boot event(s) recorded", pxe.BootEvents))
	}
}

func (h Handler) fillBMCLabValidation(report *labValidationReport) {
	bmc := labValidationBMC{Adapter: h.bmc.AdapterName()}
	h.db.Model(&models.BmcEndpoint{}).Count(&bmc.Total)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ?", "ok").Count(&bmc.OK)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ?", "error").Count(&bmc.Error)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ? OR status = ''", "unknown").Count(&bmc.Unknown)
	var latest models.BmcEndpoint
	if err := h.db.Where("last_checked_at IS NOT NULL").Order("last_checked_at desc").First(&latest).Error; err == nil {
		bmc.LastCheckedAt = latest.LastCheckedAt
	}
	var endpoints []models.BmcEndpoint
	h.db.Order("updated_at desc, id desc").Limit(10).Find(&endpoints)
	bmc.RecentEndpoints = make([]labBMCRef, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ref := h.labServerRef(endpoint.ServerID)
		bmc.RecentEndpoints = append(bmc.RecentEndpoints, labBMCRef{
			ServerID: endpoint.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Type: endpoint.Type, Protocol: endpoint.Protocol,
			Endpoint: endpoint.Endpoint, Status: endpoint.Status, PowerState: endpoint.PowerState, LastCheckedAt: endpoint.LastCheckedAt, UpdatedAt: endpoint.UpdatedAt,
		})
	}
	report.BMC = bmc

	if bmc.Adapter == "simulated" {
		report.addCheck("bmc_adapter", "warning", "BMC_ADAPTER=simulated; physical Redfish/IPMI checks are not active")
	} else {
		report.addCheck("bmc_adapter", "ok", "physical BMC adapter is configured: "+bmc.Adapter)
	}
	switch {
	case bmc.Total == 0:
		report.addCheck("bmc_connectivity", "warning", "no BMC endpoint is configured")
	case bmc.Error > 0:
		report.addCheck("bmc_connectivity", "error", fmt.Sprintf("%d BMC endpoint(s) are in error", bmc.Error))
	case bmc.Unknown > 0:
		report.addCheck("bmc_connectivity", "warning", fmt.Sprintf("%d BMC endpoint(s) have not passed connectivity check", bmc.Unknown))
	default:
		report.addCheck("bmc_connectivity", "ok", fmt.Sprintf("%d BMC endpoint(s) passed connectivity check", bmc.OK))
	}
}

func (h Handler) fillSSHLabValidation(report *labValidationReport) {
	ssh := labValidationSSH{CollectorMode: h.cfg.CollectorMode, OperationsMode: h.cfg.SSHOperationsMode}
	h.db.Model(&models.SSHAccess{}).Count(&ssh.Total)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "ok").Count(&ssh.OK)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "error").Count(&ssh.Error)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "configured").Count(&ssh.Configured)
	h.db.Model(&models.SSHAccess{}).Where("status = ? OR status = ''", "unknown").Count(&ssh.Unknown)
	var latest models.SSHAccess
	if err := h.db.Where("last_checked_at IS NOT NULL").Order("last_checked_at desc").First(&latest).Error; err == nil {
		ssh.LastCheckedAt = latest.LastCheckedAt
	}
	var accesses []models.SSHAccess
	h.db.Order("updated_at desc, id desc").Limit(10).Find(&accesses)
	ssh.RecentSSHAccesses = make([]labSSHRef, 0, len(accesses))
	for _, access := range accesses {
		ref := h.labServerRef(access.ServerID)
		ssh.RecentSSHAccesses = append(ssh.RecentSSHAccesses, labSSHRef{
			ServerID: access.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Host: access.Host, Port: access.Port,
			Username: access.Username, AuthType: access.AuthType, Status: access.Status, LastCheckedAt: access.LastCheckedAt, UpdatedAt: access.UpdatedAt,
		})
	}
	report.SSH = ssh

	if strings.ToLower(strings.TrimSpace(ssh.CollectorMode)) != "ssh" && strings.ToLower(strings.TrimSpace(ssh.OperationsMode)) != "ssh" {
		report.addCheck("ssh_modes", "warning", "COLLECTOR_MODE and SSH_OPERATIONS_MODE are not using ssh")
	} else {
		report.addCheck("ssh_modes", "ok", "SSH-backed collection or operations mode is enabled")
	}
	switch {
	case ssh.Total == 0:
		report.addCheck("ssh_connectivity", "warning", "no SSH access is configured")
	case ssh.Error > 0:
		report.addCheck("ssh_connectivity", "error", fmt.Sprintf("%d SSH target(s) are in error", ssh.Error))
	case ssh.Configured+ssh.Unknown > 0:
		report.addCheck("ssh_connectivity", "warning", fmt.Sprintf("%d SSH target(s) have not passed connectivity check", ssh.Configured+ssh.Unknown))
	default:
		report.addCheck("ssh_connectivity", "ok", fmt.Sprintf("%d SSH target(s) passed connectivity check", ssh.OK))
	}
}

func (h Handler) runLabPXEChecks(parent context.Context) []labValidationRunResult {
	results := make([]labValidationRunResult, 0, 3)
	results = append(results, h.runLabPXEHTTPCheck(parent))
	results = append(results, h.runLabPXEDHCPCheck(parent))
	results = append(results, h.runLabPXETFTPCheck(parent))
	return results
}

func (h Handler) runLabPXEHTTPCheck(parent context.Context) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_http", Hostname: "PXE HTTP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	probeURL, err := labIPXEProbeURL(h.cfg.BootBaseURL)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		result.Message = fmt.Sprintf("BOOT_BASE_URL /boot/ipxe returned HTTP %d", res.StatusCode)
		return result
	}
	if !strings.Contains(string(body), "#!ipxe") {
		result.Message = "BOOT_BASE_URL /boot/ipxe did not return an iPXE script"
		return result
	}
	result.Status = "success"
	result.Message = "BOOT_BASE_URL /boot/ipxe returned a valid iPXE script"
	return result
}

func (h Handler) runLabPXEDHCPCheck(parent context.Context) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_dhcp", Hostname: "PXE DHCP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	if !h.cfg.BootServicesEnabled {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICES_ENABLED=false; DHCP/ProxyDHCP listener is not expected to be active"
		return result
	}
	if strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) == "external" {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICE_MODE=external; DHCP is expected to be provided outside the platform"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	probe, err := services.ProbePXEDHCP(ctx, h.cfg.BootDHCPListenAddr, "52:54:00:00:00:fe", 9)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.Status = "success"
	detail := fmt.Sprintf("DHCP/ProxyDHCP returned bootfile %s", probe.Bootfile)
	if probe.ServerIP != "" {
		detail += " from " + probe.ServerIP
	}
	if probe.LeaseIP != "" {
		detail += " lease " + probe.LeaseIP
	}
	result.Message = detail
	return result
}

func (h Handler) runLabPXETFTPCheck(parent context.Context) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_tftp", Hostname: "PXE TFTP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	if !h.cfg.BootServicesEnabled {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICES_ENABLED=false; TFTP listener is not expected to be active"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	data, err := services.ProbeTFTPFile(ctx, h.cfg.BootTFTPListenAddr, "boot.ipxe", 64*1024)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if !strings.Contains(string(data), "#!ipxe") {
		result.Message = "TFTP boot.ipxe did not return an iPXE script"
		return result
	}
	result.Status = "success"
	result.Message = "TFTP boot.ipxe returned a valid iPXE script"
	return result
}

func labIPXEProbeURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid BOOT_BASE_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("BOOT_BASE_URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("BOOT_BASE_URL must include host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/boot/ipxe"
	parsed.RawQuery = url.Values{
		"mac":      []string{"52:54:00:00:00:fe"},
		"arch":     []string{"x86_64"},
		"firmware": []string{"uefi"},
	}.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

type labServerReference struct {
	Hostname string
	AssetNo  string
	Status   string
}

func (h Handler) labServerRef(serverID uint) labServerReference {
	var server models.Server
	if err := h.db.Select("hostname", "asset_no", "status").First(&server, serverID).Error; err != nil {
		return labServerReference{}
	}
	return labServerReference{Hostname: server.Hostname, AssetNo: server.AssetNo, Status: server.Status}
}

func (h Handler) runLabBMCChecks(parent context.Context, limit int) []labValidationRunResult {
	var endpoints []models.BmcEndpoint
	h.db.Order("updated_at desc, id desc").Limit(limit).Find(&endpoints)
	results := make([]labValidationRunResult, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ref := h.labServerRef(endpoint.ServerID)
		result := labValidationRunResult{Kind: "bmc", ServerID: endpoint.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "failed"}
		if h.bmc.AdapterName() == "simulated" {
			result.Status = "skipped"
			result.Message = "BMC_ADAPTER=simulated; set redfish or ipmi before running physical validation"
			results = append(results, result)
			continue
		}
		if terminalServerStatus(ref.Status) {
			result.Status = "skipped"
			result.Message = "terminal lifecycle server is skipped"
			results = append(results, result)
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		checked, err := h.bmc.Check(ctx, strconv.FormatUint(uint64(endpoint.ServerID), 10))
		cancel()
		result.CheckedAt = checked.LastCheckedAt
		if err != nil {
			result.Message = err.Error()
			results = append(results, result)
			continue
		}
		result.Status = "success"
		result.Message = "BMC connectivity check passed"
		results = append(results, result)
	}
	return results
}

func (h Handler) runLabSSHChecks(parent context.Context, limit int) []labValidationRunResult {
	var accesses []models.SSHAccess
	h.db.Order("updated_at desc, id desc").Limit(limit).Find(&accesses)
	results := make([]labValidationRunResult, 0, len(accesses))
	for _, access := range accesses {
		ref := h.labServerRef(access.ServerID)
		result := labValidationRunResult{Kind: "ssh", ServerID: access.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "failed"}
		if terminalServerStatus(ref.Status) {
			result.Status = "skipped"
			result.Message = "terminal lifecycle server is skipped"
			results = append(results, result)
			continue
		}
		ctx, cancel := context.WithTimeout(parent, h.cfg.SSHConnectTimeout+5*time.Second)
		checked, err := h.sshExecutor.Check(ctx, access.ServerID)
		cancel()
		result.CheckedAt = checked.LastCheckedAt
		if err != nil {
			result.Message = err.Error()
			results = append(results, result)
			continue
		}
		result.Status = "success"
		result.Message = "SSH connectivity check passed"
		results = append(results, result)
	}
	return results
}
