package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
)

type metadataContext struct {
	Server     models.Server
	Deployment *models.Deployment
	Token      models.MetadataToken
}

func (h Handler) renderIPXE(c *gin.Context) {
	req := services.BootRequest{MAC: c.Query("mac"), Architecture: c.DefaultQuery("arch", "x86_64"), Firmware: c.DefaultQuery("firmware", "uefi"), RemoteAddr: c.ClientIP()}
	if req.MAC == "" {
		c.String(http.StatusBadRequest, "missing mac")
		return
	}
	script, _, err := h.boot.RenderIPXEScript(req)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, script)
}

func (h Handler) discoveryIPXE(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, h.boot.DiscoveryScript())
}

func (h Handler) linuxInstallerIPXE(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, h.boot.LinuxInstallerScript())
}

func (h Handler) recordBootEvent(c *gin.Context) {
	var req struct {
		MAC          string `json:"mac" binding:"required"`
		Architecture string `json:"architecture"`
		Firmware     string `json:"firmware"`
	}
	if !bind(c, &req) {
		return
	}
	_, event, err := h.boot.RenderIPXEScript(services.BootRequest{MAC: req.MAC, Architecture: req.Architecture, Firmware: req.Firmware, RemoteAddr: c.ClientIP()})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, event)
}

func (h Handler) metadataInstanceID(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "instance-id", "by-server") {
		return
	}
	server := h.serverFromMetadata(c)
	if server == nil {
		return
	}
	h.recordMetadataAccess(c, server.ID, nil, "instance-id", "by-server")
	c.String(http.StatusOK, fmt.Sprintf("server-%d", server.ID))
}

func (h Handler) metadataHostname(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "hostname", "by-server") {
		return
	}
	server := h.serverFromMetadata(c)
	if server == nil {
		return
	}
	h.recordMetadataAccess(c, server.ID, nil, "hostname", "by-server")
	c.String(http.StatusOK, server.Hostname)
}

func (h Handler) metadataNetwork(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "network", "by-server") {
		return
	}
	server := h.serverFromMetadata(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	var deploymentID *uint
	if deployment != nil {
		deploymentID = &deployment.ID
	}
	h.recordMetadataAccess(c, server.ID, deploymentID, "network", "by-server")
	h.renderMetadataNetwork(c, server, deployment)
}

func (h Handler) metadataUserdata(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "userdata", "by-server") {
		return
	}
	server := h.serverFromMetadata(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	var deploymentID *uint
	if deployment != nil {
		deploymentID = &deployment.ID
	}
	token, err := h.boot.EnsureMetadataToken(server.ID, deploymentID)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	h.db.Model(&token).Update("last_used_at", now)
	h.recordMetadataAccess(c, server.ID, deploymentID, "userdata", "by-server")
	h.renderUserdata(c, server, deployment, token.Token)
}

func (h Handler) metadataSSHKeys(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "ssh-keys", "by-server") {
		return
	}
	server := h.serverFromMetadata(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	var deploymentID *uint
	if deployment != nil {
		deploymentID = &deployment.ID
	}
	h.recordMetadataAccess(c, server.ID, deploymentID, "ssh-keys", "by-server")
	h.renderMetadataSSHKeys(c, deployment)
}

func (h Handler) metadataInstanceIDByToken(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "instance-id", "by-token") {
		return
	}
	ctx := h.metadataFromToken(c)
	if ctx == nil {
		return
	}
	h.recordMetadataAccess(c, ctx.Server.ID, ctx.Token.DeploymentID, "instance-id", "by-token")
	c.String(http.StatusOK, fmt.Sprintf("server-%d", ctx.Server.ID))
}

func (h Handler) metadataHostnameByToken(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "hostname", "by-token") {
		return
	}
	ctx := h.metadataFromToken(c)
	if ctx == nil {
		return
	}
	h.recordMetadataAccess(c, ctx.Server.ID, ctx.Token.DeploymentID, "hostname", "by-token")
	c.String(http.StatusOK, ctx.Server.Hostname)
}

func (h Handler) metadataNetworkByToken(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "network", "by-token") {
		return
	}
	ctx := h.metadataFromToken(c)
	if ctx == nil {
		return
	}
	h.recordMetadataAccess(c, ctx.Server.ID, ctx.Token.DeploymentID, "network", "by-token")
	h.renderMetadataNetwork(c, &ctx.Server, ctx.Deployment)
}

func (h Handler) metadataUserdataByToken(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "userdata", "by-token") {
		return
	}
	ctx := h.metadataFromToken(c)
	if ctx == nil {
		return
	}
	h.recordMetadataAccess(c, ctx.Server.ID, ctx.Token.DeploymentID, "userdata", "by-token")
	h.renderUserdata(c, &ctx.Server, ctx.Deployment, ctx.Token.Token)
}

func (h Handler) metadataSSHKeysByToken(c *gin.Context) {
	if !h.ensureMetadataNetworkAccess(c, "ssh-keys", "by-token") {
		return
	}
	ctx := h.metadataFromToken(c)
	if ctx == nil {
		return
	}
	h.recordMetadataAccess(c, ctx.Server.ID, ctx.Token.DeploymentID, "ssh-keys", "by-token")
	h.renderMetadataSSHKeys(c, ctx.Deployment)
}

func (h Handler) metadataInstanceIDByClientIP(c *gin.Context) {
	h.metadataFieldByClientIP(c, "instance-id")
}

func (h Handler) metadataHostnameByClientIP(c *gin.Context) {
	h.metadataFieldByClientIP(c, "hostname")
}

func (h Handler) metadataNetworkByClientIP(c *gin.Context) {
	h.metadataFieldByClientIP(c, "network")
}

func (h Handler) metadataSSHKeysByClientIP(c *gin.Context) {
	h.metadataFieldByClientIP(c, "ssh-keys")
}

func (h Handler) metadataUserdataByClientIP(c *gin.Context) {
	h.metadataFieldByClientIP(c, "userdata")
}

func (h Handler) metadataFieldByClientIP(c *gin.Context, field string) {
	if !h.ensureMetadataNetworkAccess(c, field, "by-client-ip") {
		return
	}
	server := h.serverFromMetadataClientIP(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	h.respondMetadataField(c, server, deployment, field, "by-client-ip")
}

func (h Handler) metadataByMAC(c *gin.Context) {
	h.metadataFieldByMAC(c, c.Param("field"))
}

func (h Handler) metadataUserdataByMAC(c *gin.Context) {
	h.metadataFieldByMAC(c, "userdata")
}

func (h Handler) metadataFieldByMAC(c *gin.Context, field string) {
	if !h.ensureMetadataNetworkAccess(c, field, "by-mac") {
		return
	}
	server := h.serverFromMetadataMAC(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	h.respondMetadataField(c, server, deployment, field, "by-mac")
}

func (h Handler) metadataByIP(c *gin.Context) {
	h.metadataFieldByIP(c, c.Param("field"))
}

func (h Handler) metadataUserdataByIP(c *gin.Context) {
	h.metadataFieldByIP(c, "userdata")
}

func (h Handler) metadataFieldByIP(c *gin.Context, field string) {
	if !h.ensureMetadataNetworkAccess(c, field, "by-ip") {
		return
	}
	server := h.serverFromMetadataIP(c)
	if server == nil {
		return
	}
	deployment := h.latestActiveDeployment(server.ID)
	h.respondMetadataField(c, server, deployment, field, "by-ip")
}

func (h Handler) metadataByDeployment(c *gin.Context) {
	h.metadataFieldByDeployment(c, c.Param("field"))
}

func (h Handler) metadataUserdataByDeployment(c *gin.Context) {
	h.metadataFieldByDeployment(c, "userdata")
}

func (h Handler) metadataFieldByDeployment(c *gin.Context, field string) {
	if !h.ensureMetadataNetworkAccess(c, field, "by-deployment") {
		return
	}
	deployment, server := h.metadataDeploymentContext(c)
	if deployment == nil || server == nil {
		return
	}
	h.respondMetadataField(c, server, deployment, field, "by-deployment")
}

func (h Handler) respondMetadataField(c *gin.Context, server *models.Server, deployment *models.Deployment, field string, mode string) {
	field = strings.TrimSpace(field)
	var deploymentID *uint
	if deployment != nil {
		deploymentID = &deployment.ID
	}
	switch field {
	case "instance-id":
		h.recordMetadataAccess(c, server.ID, deploymentID, "instance-id", mode)
		c.String(http.StatusOK, fmt.Sprintf("server-%d", server.ID))
	case "hostname":
		h.recordMetadataAccess(c, server.ID, deploymentID, "hostname", mode)
		c.String(http.StatusOK, server.Hostname)
	case "network":
		h.recordMetadataAccess(c, server.ID, deploymentID, "network", mode)
		h.renderMetadataNetwork(c, server, deployment)
	case "ssh-keys":
		h.recordMetadataAccess(c, server.ID, deploymentID, "ssh-keys", mode)
		h.renderMetadataSSHKeys(c, deployment)
	case "userdata":
		token, err := h.boot.EnsureMetadataToken(server.ID, deploymentID)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now().UTC()
		h.db.Model(&token).Update("last_used_at", now)
		h.recordMetadataAccess(c, server.ID, deploymentID, "userdata", mode)
		h.renderUserdata(c, server, deployment, token.Token)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "metadata field not found"})
	}
}

func (h Handler) recordMetadataAccess(c *gin.Context, serverID uint, deploymentID *uint, endpoint string, mode string) {
	message := fmt.Sprintf("metadata access endpoint=%s mode=%s client_ip=%s", endpoint, mode, c.ClientIP())
	if deploymentID != nil {
		message = fmt.Sprintf("%s deployment_id=%d", message, *deploymentID)
	}
	now := time.Now().UTC()
	_ = h.db.Create(&models.LogEvent{
		ServerID:   serverID,
		Source:     "metadata",
		Level:      "info",
		Message:    message,
		TraceID:    middleware.RequestIDValue(c),
		OccurredAt: now,
	}).Error
}

func (h Handler) ensureMetadataNetworkAccess(c *gin.Context, endpoint string, mode string) bool {
	if !h.cfg.MetadataRequireDeploy {
		return true
	}
	clientIPText := c.ClientIP()
	clientIP := net.ParseIP(clientIPText)
	if clientIP == nil || !h.clientInEnabledDeploymentNetwork(clientIP) {
		h.recordMetadataAccessDenied(c, endpoint, mode, clientIPText)
		c.JSON(http.StatusForbidden, gin.H{"error": "metadata access is only allowed from enabled deployment networks"})
		return false
	}
	return true
}

func (h Handler) clientInEnabledDeploymentNetwork(clientIP net.IP) bool {
	var networks []models.NetworkConfig
	if err := h.db.Where("purpose = ? AND status = ?", "deployment", "enabled").Find(&networks).Error; err != nil {
		return false
	}
	for _, network := range networks {
		_, cidr, err := net.ParseCIDR(strings.TrimSpace(network.CIDR))
		if err == nil && cidr.Contains(clientIP) {
			return true
		}
	}
	return false
}

func (h Handler) recordMetadataAccessDenied(c *gin.Context, endpoint string, mode string, clientIP string) {
	now := time.Now().UTC()
	message := fmt.Sprintf("metadata access denied endpoint=%s mode=%s client_ip=%s", endpoint, mode, clientIP)
	_ = h.db.Create(&models.LogEvent{
		Source:     "metadata",
		Level:      "warning",
		Message:    message,
		TraceID:    middleware.RequestIDValue(c),
		OccurredAt: now,
	}).Error
}

func (h Handler) renderUserdata(c *gin.Context, server *models.Server, deployment *models.Deployment, metadataToken string) {
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	if deployment != nil && deployment.TemplateID != nil {
		var tmpl models.InstallTemplate
		if err := h.db.First(&tmpl, *deployment.TemplateID).Error; err == nil && tmpl.Content != "" {
			c.String(http.StatusOK, services.RenderInstallTemplate(tmpl.Content, *server, *deployment, metadataToken))
			return
		}
	}
	c.String(http.StatusOK, "#!/bin/sh\nset -eu\necho 'Registering %s with baremetal platform'\necho 'metadata token: %s'\n", server.Hostname, metadataToken)
}

func (h Handler) renderMetadataNetwork(c *gin.Context, server *models.Server, deployment *models.Deployment) {
	var network models.NetworkConfig
	payload := gin.H{
		"name":    "primary",
		"mac":     server.PrimaryMAC,
		"address": server.PrimaryIP,
		"gateway": "",
		"dns":     []string{},
	}
	if selected, ok := h.metadataNetworkForDeployment(deployment); ok {
		network = selected
		payload["network_id"] = network.ID
		payload["network_name"] = network.Name
		payload["cidr"] = network.CIDR
		payload["gateway"] = network.Gateway
		payload["dns"] = splitDNS(network.DNS)
		payload["vlan_id"] = network.VLANID
		payload["dhcp_mode"] = network.DHCPMode
		payload["proxy_dhcp"] = network.ProxyDHCP
	}
	c.JSON(http.StatusOK, gin.H{"interfaces": []gin.H{payload}})
}

func (h Handler) renderMetadataSSHKeys(c *gin.Context, deployment *models.Deployment) {
	c.JSON(http.StatusOK, gin.H{"keys": metadataSSHKeys(deployment)})
}

func (h Handler) metadataNetworkForDeployment(deployment *models.Deployment) (models.NetworkConfig, bool) {
	var network models.NetworkConfig
	if deployment != nil && deployment.NetworkID != nil {
		if err := h.db.First(&network, *deployment.NetworkID).Error; err == nil {
			return network, true
		}
	}
	if err := h.db.Where("purpose = ? AND status = ?", "deployment", "enabled").Order("updated_at desc, id desc").First(&network).Error; err == nil {
		return network, true
	}
	return models.NetworkConfig{}, false
}

func splitDNS(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func metadataSSHKeys(deployment *models.Deployment) []string {
	if deployment == nil || len(deployment.Variables) == 0 {
		return []string{}
	}
	var variables map[string]any
	if err := json.Unmarshal(deployment.Variables, &variables); err != nil {
		return []string{}
	}
	keys := []string{}
	keys = appendSSHKeyValue(keys, variables["ssh_authorized_keys"])
	keys = appendSSHKeyValue(keys, variables["ssh_keys"])
	keys = appendSSHKeyValue(keys, variables["ssh_public_key"])
	return uniqueNonEmptyStrings(keys)
}

func appendSSHKeyValue(keys []string, value any) []string {
	switch typed := value.(type) {
	case string:
		for _, line := range strings.Split(typed, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				keys = append(keys, trimmed)
			}
		}
	case []any:
		for _, item := range typed {
			keys = appendSSHKeyValue(keys, item)
		}
	}
	return keys
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (h Handler) serverFromMetadata(c *gin.Context) *models.Server {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return nil
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(id)).Error) {
		return nil
	}
	return &server
}

func (h Handler) serverFromMetadataMAC(c *gin.Context) *models.Server {
	mac := normalizeMetadataMAC(c.Param("mac"))
	if mac == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mac"})
		return nil
	}
	var server models.Server
	if notFound(c, h.db.Where("lower(primary_mac) = ?", mac).First(&server).Error) {
		return nil
	}
	return &server
}

func (h Handler) serverFromMetadataIP(c *gin.Context) *models.Server {
	ip := strings.TrimSpace(c.Param("ip"))
	if net.ParseIP(ip) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ip"})
		return nil
	}
	var server models.Server
	if notFound(c, h.db.Where("primary_ip = ?", ip).First(&server).Error) {
		return nil
	}
	return &server
}

func (h Handler) serverFromMetadataClientIP(c *gin.Context) *models.Server {
	ip := strings.TrimSpace(c.ClientIP())
	if net.ParseIP(ip) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client ip"})
		return nil
	}
	var server models.Server
	if notFound(c, h.db.Where("primary_ip = ?", ip).First(&server).Error) {
		return nil
	}
	return &server
}

func (h Handler) metadataDeploymentContext(c *gin.Context) (*models.Deployment, *models.Server) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deployment id"})
		return nil, nil
	}
	var deployment models.Deployment
	if notFound(c, h.db.First(&deployment, uint(id)).Error) {
		return nil, nil
	}
	var server models.Server
	if notFound(c, h.db.First(&server, deployment.ServerID).Error) {
		return nil, nil
	}
	return &deployment, &server
}

func normalizeMetadataMAC(mac string) string {
	value := strings.TrimSpace(mac)
	if value == "" {
		return ""
	}
	if parsed, err := net.ParseMAC(value); err == nil {
		return strings.ToLower(parsed.String())
	}
	return strings.ToLower(value)
}

func (h Handler) latestActiveDeploymentID(serverID uint) *uint {
	deployment := h.latestActiveDeployment(serverID)
	if deployment == nil {
		return nil
	}
	return &deployment.ID
}

func (h Handler) latestActiveDeployment(serverID uint) *models.Deployment {
	var deployment models.Deployment
	if err := h.db.Where("server_id = ? AND status IN ?", serverID, []string{"pending", "running"}).Order("created_at desc").First(&deployment).Error; err != nil {
		return nil
	}
	return &deployment
}

func (h Handler) metadataFromToken(c *gin.Context) *metadataContext {
	rawToken := strings.TrimSpace(c.Param("token"))
	if rawToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing metadata token"})
		return nil
	}
	var token models.MetadataToken
	if notFound(c, h.db.Where("token = ?", rawToken).First(&token).Error) {
		return nil
	}
	if token.ExpiresAt != nil && time.Now().UTC().After(*token.ExpiresAt) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "metadata token expired"})
		return nil
	}
	var server models.Server
	if notFound(c, h.db.First(&server, token.ServerID).Error) {
		return nil
	}
	var deployment *models.Deployment
	if token.DeploymentID != nil {
		var row models.Deployment
		if notFound(c, h.db.First(&row, *token.DeploymentID).Error) {
			return nil
		}
		deployment = &row
	}
	now := time.Now().UTC()
	h.db.Model(&token).Update("last_used_at", now)
	token.LastUsedAt = &now
	return &metadataContext{Server: server, Deployment: deployment, Token: token}
}
