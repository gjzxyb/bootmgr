package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	defaultRetirementReason  = "manual retirement"
	defaultScrapReason       = "manual scrap"
	maxRetirementReasonLen   = 500
	maxRetirementMethodLen   = 120
	maxRetirementEvidenceLen = 2000
)

type retireServerRequest struct {
	Reason      string `json:"reason"`
	EraseStatus string `json:"erase_status"`
	EraseMethod string `json:"erase_method"`
	Evidence    string `json:"evidence"`
}

type serverDeletionBlocker struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func (h Handler) listServers(c *gin.Context) {
	query := h.db.Model(&models.Server{})
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("owner"); value != "" {
		query = query.Where("owner = ?", value)
	}
	if value := c.Query("tenant_id"); value != "" {
		query = query.Where("tenant_id = ?", value)
	}
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("asset_no LIKE ? OR hostname LIKE ? OR primary_ip LIKE ? OR primary_mac LIKE ? OR serial_number LIKE ? OR CAST(tags AS TEXT) LIKE ?", like, like, like, like, like, like)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.Server
		query.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.Server
	query.Order("updated_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createServer(c *gin.Context) {
	var row models.Server
	if !bind(c, &row) {
		return
	}
	now := time.Now().UTC()
	row.ID = 0
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	row.DeletedAt = gorm.DeletedAt{}
	if err := normalizeServer(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateActiveTenant(h.db, row.TenantID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := enforceTenantServerQuota(h.db, row.TenantID, 0); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ensureServerUnique(h.db, row, -1, 0); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row.DiscoveredAt = &now
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	_ = h.statusHistory.Record(row.ID, "", row.Status, "server.create", email)
	h.audit.Record(id, email, "server.create", "server", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) importServers(c *gin.Context) {
	var req struct {
		Servers []models.Server `json:"servers" binding:"required"`
	}
	if !bind(c, &req) {
		return
	}
	if len(req.Servers) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "servers cannot be empty"})
		return
	}
	if len(req.Servers) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot import more than 500 servers at once"})
		return
	}
	now := time.Now().UTC()
	id, email := middleware.Actor(c)
	created := make([]models.Server, 0, len(req.Servers))
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		for i, server := range req.Servers {
			server.ID = 0
			server.CreatedAt = time.Time{}
			server.UpdatedAt = time.Time{}
			server.DeletedAt = gorm.DeletedAt{}
			server.DeployedAt = nil
			server.RetiredAt = nil
			if err := normalizeServer(&server); err != nil {
				return fmt.Errorf("servers[%d] %w", i, err)
			}
			if err := validateActiveTenant(tx, server.TenantID); err != nil {
				return fmt.Errorf("servers[%d] %w", i, err)
			}
			if err := enforceTenantServerQuota(tx, server.TenantID, 0); err != nil {
				return fmt.Errorf("servers[%d] %w", i, err)
			}
			if server.DiscoveredAt == nil {
				server.DiscoveredAt = &now
			}
			if err := ensureServerUnique(tx, server, i, 0); err != nil {
				return err
			}
			if err := tx.Create(&server).Error; err != nil {
				return err
			}
			if err := services.NewStatusHistoryService(tx).Record(server.ID, "", server.Status, "server.import", email); err != nil {
				return err
			}
			created = append(created, server)
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(id, email, "server.import", "server", fmt.Sprintf("%d", len(created)), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, gin.H{"created": len(created), "servers": created})
}

func ensureServerUnique(tx *gorm.DB, server models.Server, index int, excludeID uint) error {
	checks := []struct {
		field string
		value string
	}{{"asset_no", server.AssetNo}, {"hostname", server.Hostname}, {"primary_mac", server.PrimaryMAC}}
	for _, check := range checks {
		if check.value == "" {
			continue
		}
		var count int64
		query := tx.Model(&models.Server{}).Where("LOWER("+check.field+") = ?", strings.ToLower(check.value))
		if excludeID != 0 {
			query = query.Where("id <> ?", excludeID)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			if index >= 0 {
				return fmt.Errorf("servers[%d] %s %q already exists", index, check.field, check.value)
			}
			return fmt.Errorf("%s %q already exists", check.field, check.value)
		}
	}
	return nil
}

func validateActiveTenant(db *gorm.DB, tenantID string) error {
	_, err := activeTenant(db, tenantID)
	return err
}

func (h Handler) getServer(c *gin.Context) {
	var row models.Server
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h Handler) updateServer(c *gin.Context) {
	var row models.Server
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]json.RawMessage
	if !bind(c, &req) {
		return
	}
	beforeStatus := row.Status
	beforeTenantID := row.TenantID
	if err := applyServerPatch(&row, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeServer(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if beforeStatus != row.Status && terminalServerStatus(row.Status) && c.GetHeader("X-Confirm-Action") != "server.status-terminal" {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "confirmation required", "required_header": "X-Confirm-Action", "required_value": "server.status-terminal"})
		return
	}
	if err := validateActiveTenant(h.db, row.TenantID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if row.TenantID != "" && row.TenantID != beforeTenantID {
		if err := enforceTenantServerQuota(h.db, row.TenantID, row.ID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if err := ensureServerUnique(h.db, row, -1, row.ID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	if beforeStatus != row.Status {
		_ = h.statusHistory.Record(row.ID, beforeStatus, row.Status, "server.update", email)
	}
	h.audit.Record(id, email, "server.update", "server", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func (h Handler) deleteServer(c *gin.Context) {
	var row models.Server
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	blockers, err := h.serverDeletionBlockers(row.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(blockers) > 0 {
		problems := make([]string, 0, len(blockers))
		for _, blocker := range blockers {
			problems = append(problems, fmt.Sprintf("%s: %d", blocker.Name, blocker.Count))
		}
		c.JSON(http.StatusConflict, gin.H{"error": "server has related records; retire or scrap it instead, or remove related configuration first", "blockers": blockers, "problems": problems})
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("server_id = ?", row.ID).Delete(&models.ServerStatusHistory{}).Error; err != nil {
			return err
		}
		return tx.Delete(&row).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "server.delete", "server", row.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.Status(http.StatusNoContent)
}

func (h Handler) serverDeletionBlockers(serverID uint) ([]serverDeletionBlocker, error) {
	checks := []struct {
		name  string
		model any
	}{
		{"deployments", &models.Deployment{}},
		{"bmc_endpoints", &models.BmcEndpoint{}},
		{"ssh_accesses", &models.SSHAccess{}},
		{"hardware_inventories", &models.HardwareInventory{}},
		{"retirement_records", &models.RetirementRecord{}},
		{"metric_samples", &models.MetricSample{}},
		{"log_events", &models.LogEvent{}},
		{"collection_jobs", &models.CollectionJob{}},
		{"script_executions", &models.ScriptExecution{}},
		{"terminal_sessions", &models.TerminalSession{}},
		{"metadata_tokens", &models.MetadataToken{}},
		{"alerts", &models.Alert{}},
	}
	blockers := make([]serverDeletionBlocker, 0)
	for _, check := range checks {
		var count int64
		if err := h.db.Model(check.model).Where("server_id = ?", serverID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count > 0 {
			blockers = append(blockers, serverDeletionBlocker{Name: check.name, Count: count})
		}
	}
	return blockers, nil
}

func normalizeServer(row *models.Server) error {
	row.AssetNo = strings.TrimSpace(row.AssetNo)
	row.Hostname = strings.ToLower(strings.TrimSpace(row.Hostname))
	row.Status = strings.TrimSpace(row.Status)
	row.Architecture = strings.TrimSpace(row.Architecture)
	row.SerialNumber = strings.TrimSpace(row.SerialNumber)
	row.MotherboardUUID = strings.TrimSpace(row.MotherboardUUID)
	row.PrimaryIP = strings.TrimSpace(row.PrimaryIP)
	row.PrimaryMAC = strings.TrimSpace(row.PrimaryMAC)
	row.TenantID = strings.TrimSpace(row.TenantID)
	row.Owner = strings.TrimSpace(row.Owner)
	row.Location = strings.TrimSpace(row.Location)
	row.Rack = strings.TrimSpace(row.Rack)
	row.RackUnit = strings.TrimSpace(row.RackUnit)
	if row.Status == "" {
		row.Status = "discovered"
	}
	if row.Architecture == "" {
		row.Architecture = "x86_64"
	}
	if row.AssetNo == "" && row.Hostname == "" && row.PrimaryMAC == "" {
		return fmt.Errorf("server must include asset_no, hostname, or primary_mac")
	}
	if !stringIn(row.Status, "discovered", "ready", "deploying", "running", "maintenance", "retired", "scrapped") {
		return fmt.Errorf("status must be one of discovered, ready, deploying, running, maintenance, retired, scrapped")
	}
	if !stringIn(row.Architecture, "x86_64", "arm64") {
		return fmt.Errorf("architecture must be x86_64 or arm64")
	}
	if row.PrimaryIP != "" && net.ParseIP(row.PrimaryIP) == nil {
		return fmt.Errorf("primary_ip must be a valid IP address")
	}
	if row.PrimaryMAC != "" {
		parsed, err := net.ParseMAC(row.PrimaryMAC)
		if err != nil {
			return fmt.Errorf("primary_mac must be a valid MAC address")
		}
		row.PrimaryMAC = strings.ToLower(parsed.String())
	}
	if tags, err := normalizeOptionalJSON(row.Tags, "tags"); err != nil {
		return err
	} else {
		row.Tags = tags
	}
	return nil
}

func applyServerPatch(row *models.Server, req map[string]json.RawMessage) error {
	for key, raw := range req {
		switch key {
		case "id", "created_at", "updated_at", "deleted_at", "discovered_at", "deployed_at", "retired_at":
			continue
		case "asset_no":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.AssetNo = value
		case "hostname":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Hostname = value
		case "status":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Status = value
		case "architecture":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Architecture = value
		case "serial_number":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.SerialNumber = value
		case "motherboard_uuid":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.MotherboardUUID = value
		case "primary_ip":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.PrimaryIP = value
		case "primary_mac":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.PrimaryMAC = value
		case "tenant_id":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.TenantID = value
		case "owner":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Owner = value
		case "location":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Location = value
		case "rack":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Rack = value
		case "rack_unit":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.RackUnit = value
		case "tags":
			row.Tags = jsonFromRaw(raw)
		case "notes":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Notes = value
		default:
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	return nil
}

func normalizeOptionalJSON(raw datatypes.JSON, field string) (datatypes.JSON, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON: %w", field, err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be valid JSON: %w", field, err)
	}
	return datatypes.JSON(normalized), nil
}

func normalizeRetireServerRequest(req *retireServerRequest) error {
	return normalizeTerminalLifecycleRequest(req, defaultRetirementReason)
}

func normalizeScrapServerRequest(req *retireServerRequest) error {
	return normalizeTerminalLifecycleRequest(req, defaultScrapReason)
}

func normalizeTerminalLifecycleRequest(req *retireServerRequest, defaultReason string) error {
	req.Reason = strings.TrimSpace(req.Reason)
	req.EraseStatus = strings.TrimSpace(req.EraseStatus)
	req.EraseMethod = strings.TrimSpace(req.EraseMethod)
	req.Evidence = strings.TrimSpace(req.Evidence)
	if req.Reason == "" {
		req.Reason = defaultReason
	}
	if req.EraseStatus == "" {
		req.EraseStatus = "not_required"
	}
	if err := validateRetirementText("reason", req.Reason, maxRetirementReasonLen); err != nil {
		return err
	}
	if err := validateRetirementText("erase_method", req.EraseMethod, maxRetirementMethodLen); err != nil {
		return err
	}
	if err := validateRetirementText("evidence", req.Evidence, maxRetirementEvidenceLen); err != nil {
		return err
	}
	if !validEraseStatus(req.EraseStatus) {
		return fmt.Errorf("erase_status must be one of not_required, pending, verified, failed")
	}
	if req.EraseStatus == "verified" {
		if req.EraseMethod == "" {
			return fmt.Errorf("erase_method is required when erase_status is verified")
		}
		if req.Evidence == "" {
			return fmt.Errorf("evidence is required when erase_status is verified")
		}
	}
	return nil
}

func normalizeRetirementRecord(row *models.RetirementRecord) error {
	row.FromStatus = strings.TrimSpace(row.FromStatus)
	row.ToStatus = strings.TrimSpace(row.ToStatus)
	row.Reason = strings.TrimSpace(row.Reason)
	row.EraseStatus = strings.TrimSpace(row.EraseStatus)
	row.EraseMethod = strings.TrimSpace(row.EraseMethod)
	row.Evidence = strings.TrimSpace(row.Evidence)
	row.RequestedBy = strings.TrimSpace(row.RequestedBy)
	if row.ToStatus == "" {
		row.ToStatus = "retired"
	}
	if row.Reason == "" {
		row.Reason = defaultRetirementReason
	}
	if row.EraseStatus == "" {
		row.EraseStatus = "not_required"
	}
	if row.ServerID == 0 {
		return fmt.Errorf("server_id is required")
	}
	if !stringIn(row.ToStatus, "retired", "scrapped") {
		return fmt.Errorf("to_status must be retired or scrapped")
	}
	req := retireServerRequest{Reason: row.Reason, EraseStatus: row.EraseStatus, EraseMethod: row.EraseMethod, Evidence: row.Evidence}
	defaultReason := defaultRetirementReason
	if row.ToStatus == "scrapped" {
		defaultReason = defaultScrapReason
	}
	if err := normalizeTerminalLifecycleRequest(&req, defaultReason); err != nil {
		return err
	}
	row.Reason = req.Reason
	row.EraseStatus = req.EraseStatus
	row.EraseMethod = req.EraseMethod
	row.Evidence = req.Evidence
	if err := validateRetirementText("requested_by", row.RequestedBy, 180); err != nil {
		return err
	}
	return nil
}

func validateRetirementText(field string, value string, max int) error {
	if len([]rune(value)) > max {
		return fmt.Errorf("%s cannot exceed %d characters", field, max)
	}
	return nil
}

func validEraseStatus(status string) bool {
	return stringIn(status, "not_required", "pending", "verified", "failed")
}

func terminalRecordFromRequest(serverID uint, fromStatus, toStatus string, req retireServerRequest, requestedBy string, now time.Time) models.RetirementRecord {
	return models.RetirementRecord{
		ServerID:    serverID,
		FromStatus:  fromStatus,
		ToStatus:    toStatus,
		Reason:      req.Reason,
		EraseStatus: req.EraseStatus,
		EraseMethod: req.EraseMethod,
		Evidence:    req.Evidence,
		RequestedBy: requestedBy,
		RequestedAt: now,
	}
}

func (h Handler) ensureTerminalLifecycleRecord(server models.Server, toStatus string, req retireServerRequest, requestedBy string, now time.Time, requestID string) (models.RetirementRecord, bool, error) {
	var record models.RetirementRecord
	err := h.db.Where("server_id = ? AND to_status = ?", server.ID, toStatus).Order("requested_at desc, id desc").First(&record).Error
	if err == nil {
		return record, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return record, false, err
	}
	record = terminalRecordFromRequest(server.ID, toStatus, toStatus, req, requestedBy, now)
	if err := h.db.Create(&record).Error; err != nil {
		return record, false, err
	}
	return record, true, h.db.Create(&models.LogEvent{
		ServerID:   server.ID,
		Source:     "lifecycle",
		Level:      retirementLogLevel(req.EraseStatus),
		Message:    terminalLifecycleLogMessage(server, toStatus, req),
		TraceID:    requestID,
		OccurredAt: now,
	}).Error
}

func retirementHistoryReason(reason string) string {
	return terminalHistoryReason("server.retire", reason)
}

func scrapHistoryReason(reason string) string {
	return terminalHistoryReason("server.scrap", reason)
}

func terminalHistoryReason(action string, reason string) string {
	value := action + ": " + reason
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:160])
	}
	return value
}

func retirementLogLevel(eraseStatus string) string {
	if eraseStatus == "failed" {
		return "warning"
	}
	return "info"
}

func terminalLifecycleLogMessage(server models.Server, toStatus string, req retireServerRequest) string {
	name := server.Hostname
	if name == "" {
		name = server.AssetNo
	}
	if name == "" {
		name = fmt.Sprintf("server-%d", server.ID)
	}
	return fmt.Sprintf("server %s %s: reason=%s erase_status=%s erase_method=%s", name, toStatus, req.Reason, req.EraseStatus, req.EraseMethod)
}

func (h Handler) retireServer(c *gin.Context) {
	now := time.Now().UTC()
	var req retireServerRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if !bind(c, &req) {
			return
		}
	}
	if err := normalizeRetireServerRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, c.Param("id")).Error) {
		return
	}
	id, email := middleware.Actor(c)
	if server.Status == "retired" {
		record, created, err := h.ensureTerminalLifecycleRecord(server, "retired", req, email, now, c.GetString("request_id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "record retirement: " + err.Error()})
			return
		}
		if created {
			h.audit.Record(id, email, "server.retire", "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		}
		c.JSON(http.StatusOK, gin.H{"status": "retired", "retirement": record})
		return
	}
	if server.Status == "scrapped" {
		c.JSON(http.StatusConflict, gin.H{"error": "scrapped server cannot be retired"})
		return
	}
	beforeStatus := server.Status
	record := terminalRecordFromRequest(server.ID, beforeStatus, "retired", req, email, now)
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&server).Updates(map[string]any{"status": "retired", "retired_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := services.NewStatusHistoryService(tx).Record(server.ID, beforeStatus, "retired", retirementHistoryReason(req.Reason), email); err != nil {
			return err
		}
		event := models.LogEvent{
			ServerID:   server.ID,
			Source:     "lifecycle",
			Level:      retirementLogLevel(req.EraseStatus),
			Message:    terminalLifecycleLogMessage(server, "retired", req),
			TraceID:    c.GetString("request_id"),
			OccurredAt: now,
		}
		return tx.Create(&event).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retire server: " + err.Error()})
		return
	}
	h.audit.Record(id, email, "server.retire", "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"status": "retired", "retirement": record})
}

func (h Handler) scrapServer(c *gin.Context) {
	now := time.Now().UTC()
	var req retireServerRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if !bind(c, &req) {
			return
		}
	}
	if err := normalizeScrapServerRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, c.Param("id")).Error) {
		return
	}
	id, email := middleware.Actor(c)
	if server.Status == "scrapped" {
		record, created, err := h.ensureTerminalLifecycleRecord(server, "scrapped", req, email, now, c.GetString("request_id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "record scrap: " + err.Error()})
			return
		}
		if created {
			h.audit.Record(id, email, "server.scrap", "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		}
		c.JSON(http.StatusOK, gin.H{"status": "scrapped", "retirement": record})
		return
	}
	beforeStatus := server.Status
	record := terminalRecordFromRequest(server.ID, beforeStatus, "scrapped", req, email, now)
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&server).Updates(map[string]any{"status": "scrapped", "retired_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := services.NewStatusHistoryService(tx).Record(server.ID, beforeStatus, "scrapped", scrapHistoryReason(req.Reason), email); err != nil {
			return err
		}
		event := models.LogEvent{
			ServerID:   server.ID,
			Source:     "lifecycle",
			Level:      retirementLogLevel(req.EraseStatus),
			Message:    terminalLifecycleLogMessage(server, "scrapped", req),
			TraceID:    c.GetString("request_id"),
			OccurredAt: now,
		}
		return tx.Create(&event).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scrap server: " + err.Error()})
		return
	}
	h.audit.Record(id, email, "server.scrap", "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"status": "scrapped", "retirement": record})
}

func (h Handler) getInventory(c *gin.Context) {
	var rows []models.HardwareInventory
	h.db.Where("server_id = ?", c.Param("id")).Order("collected_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listServerStatusHistory(c *gin.Context) {
	var rows []models.ServerStatusHistory
	h.db.Where("server_id = ?", c.Param("id")).Order("created_at desc, id desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listServerRetirementRecords(c *gin.Context) {
	var server models.Server
	if notFound(c, h.db.First(&server, c.Param("id")).Error) {
		return
	}
	var rows []models.RetirementRecord
	h.db.Where("server_id = ?", server.ID).Order("requested_at desc, id desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createInventory(c *gin.Context) {
	var server models.Server
	if notFound(c, h.db.First(&server, c.Param("id")).Error) {
		return
	}
	var row models.HardwareInventory
	if !bind(c, &row) {
		return
	}
	row.ID = 0
	row.ServerID = server.ID
	row.CreatedAt = time.Time{}
	row.CPUSummary = strings.TrimSpace(row.CPUSummary)
	row.MemorySummary = strings.TrimSpace(row.MemorySummary)
	row.DiskSummary = strings.TrimSpace(row.DiskSummary)
	row.NetworkSummary = strings.TrimSpace(row.NetworkSummary)
	row.GPUSummary = strings.TrimSpace(row.GPUSummary)
	row.RAIDSummary = strings.TrimSpace(row.RAIDSummary)
	row.CollectedBy = strings.TrimSpace(row.CollectedBy)
	rawPayload, err := normalizeOptionalJSONObject(row.RawPayload, "raw_payload")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row.RawPayload = rawPayload
	if row.CollectedAt.IsZero() {
		row.CollectedAt = time.Now().UTC()
	}
	if row.CollectedBy == "" {
		_, row.CollectedBy = middleware.Actor(c)
	}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "server.inventory.create", "server", server.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) getServerBOM(c *gin.Context) {
	row, err := h.bom.ForServer(c.Param("id"))
	if notFound(c, err) {
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h Handler) exportServerBOMCSV(c *gin.Context) {
	row, err := h.bom.ForServer(c.Param("id"))
	if notFound(c, err) {
		return
	}
	writeBOMCSV(c, []services.BOMRow{row}, "server-bom-"+c.Param("id")+".csv")
}

func (h Handler) exportBOMCSV(c *gin.Context) {
	rows, err := h.bom.All()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeBOMCSV(c, rows, "baremetal-bom.csv")
}

func writeBOMCSV(c *gin.Context, rows []services.BOMRow, filename string) {
	body, err := services.BOMCSV(rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.String(http.StatusOK, string(body))
}
