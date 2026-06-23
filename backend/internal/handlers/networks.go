package handlers

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h Handler) listNetworkConfigs(c *gin.Context) {
	query := h.db.Model(&models.NetworkConfig{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR cidr LIKE ? OR description LIKE ?", like, like, like)
	}
	if value := c.Query("purpose"); value != "" {
		query = query.Where("purpose = ?", value)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.NetworkConfig
		query.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.NetworkConfig
	query.Order("updated_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createNetworkConfig(c *gin.Context) {
	var row models.NetworkConfig
	if !bind(c, &row) {
		return
	}
	row.ID = 0
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	if strings.TrimSpace(row.Name) == "" || strings.TrimSpace(row.CIDR) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and cidr are required"})
		return
	}
	if row.Purpose == "" {
		row.Purpose = "deployment"
	}
	if row.DHCPMode == "" {
		row.DHCPMode = "proxy"
	}
	if row.Status == "" {
		row.Status = "enabled"
	}
	if err := validateAndNormalizeNetworkConfig(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ensureNetworkCIDRDoesNotOverlap(h.db, &row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, email := middleware.Actor(c)
	row.CreatedBy = email
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "network_config.create", "network_config", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) updateNetworkConfig(c *gin.Context) {
	var row models.NetworkConfig
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]any
	if !bind(c, &req) {
		return
	}
	delete(req, "id")
	delete(req, "created_by")
	delete(req, "created_at")
	delete(req, "updated_at")
	if err := applyNetworkConfigUpdate(&row, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ensureNetworkCIDRDoesNotOverlap(h.db, &row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.db.First(&row, row.ID)
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "network_config.update", "network_config", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

type networkCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type networkCheckReport struct {
	Status string         `json:"status"`
	Checks []networkCheck `json:"checks"`
}

func (h Handler) checkNetworkConfig(c *gin.Context) {
	var row models.NetworkConfig
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	report := networkCheckReport{Status: "ok", Checks: []networkCheck{}}
	add := func(name, status, message string) {
		report.Checks = append(report.Checks, networkCheck{Name: name, Status: status, Message: message})
		if status == "error" {
			report.Status = "error"
		} else if status == "warning" && report.Status == "ok" {
			report.Status = "warning"
		}
	}

	copy := row
	if err := validateAndNormalizeNetworkConfig(&copy); err != nil {
		add("format", "error", err.Error())
	} else {
		add("format", "ok", fmt.Sprintf("%s network %s is syntactically valid", copy.Purpose, copy.CIDR))
	}
	if err := ensureNetworkCIDRDoesNotOverlap(h.db, &copy); err != nil {
		add("overlap", "error", err.Error())
	} else if copy.Status == "enabled" {
		add("overlap", "ok", "enabled network does not overlap an enabled network with the same purpose")
	} else {
		add("overlap", "ok", "disabled network is ignored by deployment preflight overlap checks")
	}
	if strings.TrimSpace(copy.Gateway) == "" {
		if copy.Purpose == "deployment" {
			add("gateway", "warning", "deployment network has no gateway configured")
		} else {
			add("gateway", "ok", "gateway is optional for this network purpose")
		}
	} else {
		add("gateway", "ok", "gateway is a valid IP inside the CIDR")
	}
	if len(splitDNS(copy.DNS)) == 0 {
		add("dns", "warning", "no DNS server configured")
	} else {
		add("dns", "ok", "DNS server list contains valid IP addresses")
	}
	if copy.Status != "enabled" {
		add("status", "warning", "network is disabled and will not be used by deployment preflight or metadata network rendering")
	} else {
		add("status", "ok", "network is enabled")
	}
	switch copy.DHCPMode {
	case "proxy":
		if copy.ProxyDHCP {
			if h.cfg.BootServicesEnabled && strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) == "proxy" {
				add("dhcp", "ok", fmt.Sprintf("ProxyDHCP mode matches BOOT_SERVICE_MODE=proxy; DHCP listens on %s and TFTP listens on %s", h.cfg.BootDHCPListenAddr, h.cfg.BootTFTPListenAddr))
			} else {
				add("dhcp", "warning", "network expects ProxyDHCP; set BOOT_SERVICES_ENABLED=true and BOOT_SERVICE_MODE=proxy before physical PXE validation")
			}
		} else {
			add("dhcp", "warning", "dhcp_mode is proxy but proxy_dhcp is disabled")
		}
	case "builtin":
		if h.cfg.BootServicesEnabled && strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) == "builtin" {
			add("dhcp", "ok", fmt.Sprintf("builtin DHCP mode matches BOOT_SERVICE_MODE=builtin; lease range %s-%s", h.cfg.BootDHCPLeaseStart, h.cfg.BootDHCPLeaseEnd))
		} else {
			add("dhcp", "warning", "network expects builtin DHCP; set BOOT_SERVICES_ENABLED=true and BOOT_SERVICE_MODE=builtin on an isolated deployment network")
		}
	case "external":
		if h.cfg.BootServicesEnabled && strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) != "external" {
			add("dhcp", "warning", fmt.Sprintf("network expects external DHCP but platform DHCP listener mode is %s", h.cfg.BootServiceMode))
		} else {
			add("dhcp", "ok", "external DHCP mode is recorded; ensure the external DHCP service points PXE clients to the platform TFTP/HTTP endpoints")
		}
	default:
		add("dhcp", "error", "dhcp_mode must be one of proxy, builtin, external")
	}
	status, message := networkBootRuntimeStatus(h.cfg, copy)
	add("pxe_runtime", status, message)
	if copy.Purpose == "deployment" && copy.Status == "enabled" {
		add("deployment_usage", "ok", "deployment preflight can use this network as an enabled deployment network")
	} else if copy.Purpose == "deployment" {
		add("deployment_usage", "warning", "deployment preflight requires at least one enabled deployment network")
	} else {
		add("deployment_usage", "ok", "network is not a deployment network")
	}

	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "network_config.check", "network_config", row.ID, "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, report)
}

func networkBootRuntimeStatus(cfg config.Config, row models.NetworkConfig) (string, string) {
	if row.Purpose != "deployment" {
		return "ok", "PXE/DHCP/TFTP runtime checks apply to deployment networks"
	}
	if row.Status != "enabled" {
		return "warning", "deployment network is disabled; PXE clients will not use it for platform deployment"
	}
	if !cfg.BootServicesEnabled {
		return "warning", "BOOT_SERVICES_ENABLED=false; this deployment network is recorded but platform PXE/DHCP/TFTP listeners are disabled"
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.BootServiceMode))
	if mode == "" {
		mode = "proxy"
	}
	issues := config.BootRuntimeIssues(cfg)
	if len(issues) > 0 {
		parts := make([]string, 0, len(issues))
		status := "warning"
		for _, issue := range issues {
			if issue.Level == "error" {
				status = "error"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", issue.Key, issue.Message))
		}
		if status == "error" {
			return status, "PXE runtime has blocking issue(s): " + strings.Join(parts, "; ")
		}
		return status, "PXE runtime has warning(s): " + strings.Join(parts, "; ")
	}
	switch mode {
	case "proxy":
		if row.DHCPMode != "proxy" || !row.ProxyDHCP {
			return "warning", fmt.Sprintf("BOOT_SERVICE_MODE=proxy is active on %s, but this network is configured as dhcp_mode=%s proxy_dhcp=%t", cfg.BootDHCPListenAddr, row.DHCPMode, row.ProxyDHCP)
		}
		if status, message := networkPXERuntimeAddressStatus(cfg, row, mode); status != "ok" {
			return status, message
		}
		return "ok", fmt.Sprintf("ProxyDHCP and TFTP runtime are enabled on %s/%s via interface %s", cfg.BootDHCPListenAddr, cfg.BootTFTPListenAddr, cfg.BootBindInterface)
	case "builtin":
		if row.DHCPMode != "builtin" {
			return "warning", fmt.Sprintf("BOOT_SERVICE_MODE=builtin is active on %s, but this network is configured as dhcp_mode=%s", cfg.BootDHCPListenAddr, row.DHCPMode)
		}
		if status, message := networkPXERuntimeAddressStatus(cfg, row, mode); status != "ok" {
			return status, message
		}
		return "ok", fmt.Sprintf("builtin DHCP and TFTP runtime are enabled on %s/%s via interface %s", cfg.BootDHCPListenAddr, cfg.BootTFTPListenAddr, cfg.BootBindInterface)
	case "external":
		if row.DHCPMode != "external" {
			return "warning", fmt.Sprintf("BOOT_SERVICE_MODE=external starts only TFTP on %s, but this network is configured as dhcp_mode=%s", cfg.BootTFTPListenAddr, row.DHCPMode)
		}
		if status, message := networkPXERuntimeAddressStatus(cfg, row, mode); status != "ok" {
			return status, message
		}
		return "ok", fmt.Sprintf("external DHCP is expected; platform TFTP runtime is enabled on %s via interface %s", cfg.BootTFTPListenAddr, cfg.BootBindInterface)
	default:
		return "error", fmt.Sprintf("unsupported BOOT_SERVICE_MODE %q", cfg.BootServiceMode)
	}
}

func networkPXERuntimeAddressStatus(cfg config.Config, row models.NetworkConfig, mode string) (string, string) {
	prefix, err := netip.ParsePrefix(row.CIDR)
	if err != nil {
		return "warning", "deployment network CIDR is invalid; PXE runtime address alignment cannot be checked"
	}
	prefix = prefix.Masked()
	if mode != "external" {
		serverIP, err := netip.ParseAddr(strings.TrimSpace(cfg.BootDHCPServerIP))
		if err != nil || !prefix.Contains(serverIP.Unmap()) {
			return "warning", fmt.Sprintf("BOOT_DHCP_SERVER_IP %q is not inside deployment network %s", cfg.BootDHCPServerIP, prefix.String())
		}
		if addr, ok := listenAddressIP(cfg.BootDHCPListenAddr); ok && !prefix.Contains(addr.Unmap()) {
			return "warning", fmt.Sprintf("BOOT_DHCP_LISTEN_ADDR %q is not inside deployment network %s", cfg.BootDHCPListenAddr, prefix.String())
		}
	}
	if addr, ok := listenAddressIP(cfg.BootTFTPListenAddr); ok && !prefix.Contains(addr.Unmap()) {
		return "warning", fmt.Sprintf("BOOT_TFTP_LISTEN_ADDR %q is not inside deployment network %s", cfg.BootTFTPListenAddr, prefix.String())
	}
	return "ok", ""
}

func listenAddressIP(raw string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return netip.Addr{}, false
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.IsUnspecified() {
		return netip.Addr{}, false
	}
	return addr, true
}

func validateAndNormalizeNetworkConfig(row *models.NetworkConfig) error {
	row.Name = strings.TrimSpace(row.Name)
	row.Purpose = strings.TrimSpace(row.Purpose)
	row.CIDR = strings.TrimSpace(row.CIDR)
	row.Gateway = strings.TrimSpace(row.Gateway)
	row.DNS = strings.Join(splitDNS(row.DNS), ",")
	row.DHCPMode = strings.TrimSpace(row.DHCPMode)
	row.Status = strings.TrimSpace(row.Status)
	if row.Name == "" || row.CIDR == "" {
		return fmt.Errorf("name and cidr are required")
	}
	if row.Purpose == "" {
		row.Purpose = "deployment"
	}
	if row.DHCPMode == "" {
		row.DHCPMode = "proxy"
	}
	if row.Status == "" {
		row.Status = "enabled"
	}
	if !stringIn(row.Purpose, "management", "deployment", "business") {
		return fmt.Errorf("purpose must be one of management, deployment, business")
	}
	if !stringIn(row.DHCPMode, "proxy", "builtin", "external") {
		return fmt.Errorf("dhcp_mode must be one of proxy, builtin, external")
	}
	if !stringIn(row.Status, "enabled", "disabled") {
		return fmt.Errorf("status must be one of enabled, disabled")
	}
	if row.VLANID < 0 || row.VLANID > 4094 {
		return fmt.Errorf("vlan_id must be between 0 and 4094")
	}
	prefix, err := netip.ParsePrefix(row.CIDR)
	if err != nil {
		return fmt.Errorf("cidr must be a valid CIDR")
	}
	prefix = prefix.Masked()
	row.CIDR = prefix.String()
	if row.Gateway != "" {
		gateway, err := netip.ParseAddr(row.Gateway)
		if err != nil {
			return fmt.Errorf("gateway must be a valid IP address")
		}
		if !prefix.Contains(gateway) {
			return fmt.Errorf("gateway must be within cidr")
		}
	}
	for _, dns := range splitDNS(row.DNS) {
		if _, err := netip.ParseAddr(dns); err != nil {
			return fmt.Errorf("dns contains invalid IP address %q", dns)
		}
	}
	return nil
}

func ensureNetworkCIDRDoesNotOverlap(db *gorm.DB, row *models.NetworkConfig) error {
	if row.Status != "enabled" {
		return nil
	}
	prefix, err := netip.ParsePrefix(row.CIDR)
	if err != nil {
		return fmt.Errorf("cidr must be a valid CIDR")
	}
	prefix = prefix.Masked()

	query := db.Where("purpose = ? AND status = ?", row.Purpose, "enabled")
	if row.ID != 0 {
		query = query.Where("id <> ?", row.ID)
	}
	var existing []models.NetworkConfig
	if err := query.Find(&existing).Error; err != nil {
		return err
	}
	for _, network := range existing {
		existingPrefix, err := netip.ParsePrefix(network.CIDR)
		if err != nil {
			return fmt.Errorf("enabled %s network %q has invalid cidr %q", row.Purpose, network.Name, network.CIDR)
		}
		existingPrefix = existingPrefix.Masked()
		if prefix.Overlaps(existingPrefix) {
			return fmt.Errorf("enabled %s network cidr %s overlaps with %q (%s)", row.Purpose, prefix.String(), network.Name, existingPrefix.String())
		}
	}
	return nil
}

func applyNetworkConfigUpdate(row *models.NetworkConfig, req map[string]any) error {
	stringFields := []struct {
		key    string
		target *string
	}{
		{"name", &row.Name},
		{"purpose", &row.Purpose},
		{"cidr", &row.CIDR},
		{"gateway", &row.Gateway},
		{"dns", &row.DNS},
		{"dhcp_mode", &row.DHCPMode},
		{"status", &row.Status},
		{"description", &row.Description},
	}
	for _, field := range stringFields {
		raw, ok := req[field.key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if !ok && raw != nil {
			return fmt.Errorf("%s must be a string", field.key)
		}
		*field.target = strings.TrimSpace(value)
	}
	if raw, ok := req["vlan_id"]; ok {
		value, err := intFromUpdateValue(raw, "vlan_id")
		if err != nil {
			return err
		}
		row.VLANID = value
	}
	if raw, ok := req["proxy_dhcp"]; ok {
		value, ok := raw.(bool)
		if !ok && raw != nil {
			return fmt.Errorf("proxy_dhcp must be a boolean")
		}
		row.ProxyDHCP = value
	}
	if err := validateAndNormalizeNetworkConfig(row); err != nil {
		return err
	}
	return nil
}

func intFromUpdateValue(raw any, field string) (int, error) {
	if raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case float64:
		if value != math.Trunc(value) {
			return 0, fmt.Errorf("%s must be an integer", field)
		}
		return int(value), nil
	case int:
		return value, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", field)
	}
}

func stringIn(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
