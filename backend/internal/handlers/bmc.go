package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

func (h Handler) upsertBMC(c *gin.Context) {
	var req struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
		Endpoint string `json:"endpoint" binding:"required"`
		Username string `json:"username" binding:"required"`
		Password string `json:"password"`
	}
	if !bind(c, &req) {
		return
	}
	serverID := c.Param("id")
	parsedID, err := strconv.ParseUint(serverID, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(parsedID)).Error) {
		return
	}
	if terminalServerStatus(server.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "bmc operation is not allowed for retired or scrapped server"})
		return
	}
	id, email := middleware.Actor(c)
	var endpoint models.BmcEndpoint
	err = h.db.Where("server_id = ?", parsedID).First(&endpoint).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.Protocol = strings.TrimSpace(req.Protocol)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Username = strings.TrimSpace(req.Username)
	if req.Type == "" {
		req.Type = "redfish"
	}
	if req.Protocol == "" {
		if req.Type == "ipmi" {
			req.Protocol = "ipmi"
		} else {
			req.Protocol = "https"
		}
	}
	if req.Endpoint == "" || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint and username are required"})
		return
	}
	if !stringIn(req.Type, "redfish", "ipmi") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be one of redfish, ipmi"})
		return
	}
	if req.Type == "redfish" && !stringIn(req.Protocol, "http", "https") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "redfish protocol must be http or https"})
		return
	}
	if req.Type == "ipmi" && req.Protocol != "ipmi" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ipmi protocol must be ipmi"})
		return
	}
	if err := validateBMCEndpoint(req.Type, req.Endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if endpoint.PowerState == "" {
		endpoint.PowerState = "off"
	}
	if req.Password != "" {
		cred, err := h.credentials.Store("bmc-server-"+serverID, "bmc", req.Username, req.Password, email)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		endpoint.EncryptedPasswordRef = strconv.FormatUint(uint64(cred.ID), 10)
	}
	endpoint.ServerID = uint(parsedID)
	endpoint.Type = req.Type
	endpoint.Protocol = req.Protocol
	endpoint.Endpoint = req.Endpoint
	endpoint.Username = req.Username
	endpoint.Status = "unknown"
	if endpoint.ID == 0 {
		if err := h.db.Create(&endpoint).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	} else if err := h.db.Save(&endpoint).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(id, email, "bmc.upsert", "server", serverID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	endpoint.EncryptedPasswordRef = ""
	c.JSON(http.StatusOK, endpoint)
}

func (h Handler) getPower(c *gin.Context) { bmcPower(c, h, "", "") }
func (h Handler) powerOn(c *gin.Context)  { bmcPower(c, h, "on", "bmc.power-on") }
func (h Handler) powerOff(c *gin.Context) { bmcPower(c, h, "off", "bmc.power-off") }
func (h Handler) reboot(c *gin.Context)   { bmcPower(c, h, "reboot", "bmc.reboot") }

type batchBMCResult struct {
	ServerID   uint   `json:"server_id"`
	Status     string `json:"status"`
	PowerState string `json:"power_state,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (h Handler) batchPower(c *gin.Context) {
	var req struct {
		Action    string `json:"action"`
		ServerIDs []uint `json:"server_ids"`
	}
	if !bind(c, &req) {
		return
	}
	state, auditAction, confirmValue, ok := batchPowerAction(req.Action)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be one of power-on, power-off, reboot"})
		return
	}
	if c.GetHeader("X-Confirm-Action") != confirmValue {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "confirmation required", "required_header": "X-Confirm-Action", "required_value": confirmValue})
		return
	}
	serverIDs, err := normalizeBatchServerIDs(req.ServerIDs, 50)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	results := make([]batchBMCResult, 0, len(serverIDs))
	successCount := 0
	for _, serverID := range serverIDs {
		result := h.applyBatchPower(c.Request.Context(), serverID, state)
		if result.Status == "success" {
			successCount++
		}
		h.audit.Record(actorID, actorEmail, auditAction, "server", serverID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		results = append(results, result)
	}
	c.JSON(http.StatusOK, gin.H{"requested": len(serverIDs), "succeeded": successCount, "failed": len(serverIDs) - successCount, "results": results})
}

func (h Handler) applyBatchPower(parent context.Context, serverID uint, state string) batchBMCResult {
	result := batchBMCResult{ServerID: serverID, Status: "failed"}
	var server models.Server
	if err := h.db.Select("id", "status").First(&server, serverID).Error; err != nil {
		result.Error = "server not found"
		return result
	}
	if terminalServerStatus(server.Status) {
		result.Error = "bmc operation is not allowed for retired or scrapped server"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	_, powerState, err := h.bmc.SetPowerState(ctx, strconv.FormatUint(uint64(serverID), 10), state)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result.Error = "bmc endpoint not found"
		} else {
			result.Error = err.Error()
		}
		return result
	}
	result.Status = "success"
	result.PowerState = powerState
	return result
}

func batchPowerAction(action string) (state string, auditAction string, confirmValue string, ok bool) {
	switch strings.TrimSpace(action) {
	case "power-on":
		return "on", "bmc.batch-power-on", "bmc.batch-power-on", true
	case "power-off":
		return "off", "bmc.batch-power-off", "bmc.batch-power-off", true
	case "reboot":
		return "reboot", "bmc.batch-reboot", "bmc.batch-reboot", true
	default:
		return "", "", "", false
	}
}

func normalizeBatchServerIDs(ids []uint, limit int) ([]uint, error) {
	if len(ids) == 0 {
		return nil, errors.New("server_ids is required")
	}
	if len(ids) > limit {
		return nil, fmt.Errorf("server_ids cannot contain more than %d items", limit)
	}
	seen := map[uint]bool{}
	normalized := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("server_ids cannot contain 0")
		}
		if seen[id] {
			return nil, errors.New("server_ids cannot contain duplicates")
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func (h Handler) checkBMC(c *gin.Context) {
	if h.rejectTerminalServerBMCOperation(c, c.Param("id")) {
		return
	}
	id, email := middleware.Actor(c)
	endpoint, err := h.bmc.Check(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bmc endpoint not found"})
			return
		}
		h.audit.Record(id, email, "bmc.check", "server", c.Param("id"), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		c.JSON(http.StatusBadGateway, gin.H{"error": "bmc connectivity check failed", "detail": err.Error(), "status": endpoint.Status, "checked_at": endpoint.LastCheckedAt})
		return
	}
	h.audit.Record(id, email, "bmc.check", "server", c.Param("id"), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"status": endpoint.Status, "checked_at": endpoint.LastCheckedAt})
}

func (h Handler) getBMCFirmware(c *gin.Context) {
	actorID, actorEmail := middleware.Actor(c)
	_, info, err := h.bmc.FirmwareInfo(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bmc endpoint not found"})
			return
		}
		h.audit.Record(actorID, actorEmail, "bmc.firmware.read", "server", c.Param("id"), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		c.JSON(http.StatusBadGateway, gin.H{"error": "bmc firmware query failed", "detail": err.Error()})
		return
	}
	h.audit.Record(actorID, actorEmail, "bmc.firmware.read", "server", c.Param("id"), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, info)
}

func bmcPower(c *gin.Context, h Handler, next string, auditAction string) {
	var bmc models.BmcEndpoint
	var err error
	bmc, err = h.bmc.Endpoint(c.Param("id"))
	if notFound(c, err) {
		return
	}
	if next != "" {
		if h.rejectTerminalServerBMCOperation(c, c.Param("id")) {
			return
		}
		var state string
		bmc, state, err = h.bmc.SetPowerState(c.Request.Context(), c.Param("id"), next)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		bmc.PowerState = state
		id, email := middleware.Actor(c)
		h.audit.Record(id, email, auditAction, "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	}
	c.JSON(http.StatusOK, gin.H{"power_state": bmc.PowerState, "adapter": h.bmc.AdapterName()})
}

func (h Handler) rejectTerminalServerBMCOperation(c *gin.Context, serverID string) bool {
	parsedID, err := strconv.ParseUint(serverID, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return true
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(parsedID)).Error) {
		return true
	}
	if terminalServerStatus(server.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "bmc operation is not allowed for retired or scrapped server"})
		return true
	}
	return false
}

func terminalServerStatus(status string) bool {
	return status == "retired" || status == "scrapped"
}

func validateBMCEndpoint(kind, endpoint string) error {
	if kind == "redfish" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("redfish endpoint must be an http or https URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("redfish endpoint must use http or https")
		}
		if !validHostOrIP(parsed.Hostname()) {
			return fmt.Errorf("redfish endpoint host is invalid")
		}
		return nil
	}
	host := endpoint
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("ipmi endpoint host is invalid")
		}
		host = parsed.Host
	}
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	if !validHostOrIP(host) {
		return fmt.Errorf("ipmi endpoint host is invalid")
	}
	return nil
}

func validHostOrIP(value string) bool {
	value = strings.TrimSpace(strings.Trim(value, "[]"))
	if value == "" || strings.ContainsAny(value, " \t\r\n/") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	if len(value) > 253 {
		return false
	}
	return hostnamePattern.MatchString(value)
}
