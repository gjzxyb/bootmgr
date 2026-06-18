package handlers

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h Handler) upsertSSHAccess(c *gin.Context) {
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(serverID)).Error) {
		return
	}
	if server.Status == "retired" || server.Status == "scrapped" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server is retired or scrapped"})
		return
	}
	var req struct {
		Host     string `json:"host" binding:"required"`
		Port     int    `json:"port"`
		Username string `json:"username" binding:"required"`
		AuthType string `json:"auth_type"`
		Secret   string `json:"secret"`
	}
	if !bind(c, &req) {
		return
	}
	_, email := middleware.Actor(c)
	req.Host = strings.TrimSpace(req.Host)
	req.Username = strings.TrimSpace(req.Username)
	req.AuthType = strings.TrimSpace(req.AuthType)
	if req.Host == "" || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "host and username are required"})
		return
	}
	if !validHostOrIP(req.Host) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "host must be a valid hostname or IP address"})
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Port < 1 || req.Port > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "port must be between 1 and 65535"})
		return
	}
	if req.AuthType == "" {
		req.AuthType = "password"
	}
	if !stringIn(req.AuthType, "password", "private_key") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_type must be one of password, private_key"})
		return
	}
	var access models.SSHAccess
	err = h.db.Where("server_id = ?", serverID).First(&access).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Secret != "" {
		cred, err := h.credentials.Store("ssh-server-"+c.Param("id"), "ssh", req.Username, req.Secret, email)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		access.CredentialRef = strconv.FormatUint(uint64(cred.ID), 10)
	}
	access.ServerID = uint(serverID)
	access.Host = req.Host
	access.Port = req.Port
	access.Username = req.Username
	access.AuthType = req.AuthType
	access.Status = "configured"
	if access.ID == 0 {
		err = h.db.Create(&access).Error
	} else {
		err = h.db.Save(&access).Error
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "ssh.upsert", "server", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	access.CredentialRef = ""
	c.JSON(http.StatusOK, access)
}

func (h Handler) getSSHAccess(c *gin.Context) {
	var access models.SSHAccess
	if notFound(c, h.db.Where("server_id = ?", c.Param("id")).First(&access).Error) {
		return
	}
	access.CredentialRef = ""
	c.JSON(http.StatusOK, access)
}

func (h Handler) checkSSHAccess(c *gin.Context) {
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(serverID)).Error) {
		return
	}
	if server.Status == "retired" || server.Status == "scrapped" {
		c.JSON(http.StatusConflict, gin.H{"error": "server is retired or scrapped"})
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	access, err := h.sshExecutor.Check(c.Request.Context(), uint(serverID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "ssh access not found"})
			return
		}
		h.audit.Record(actorID, actorEmail, "ssh.check", "server", c.Param("id"), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		c.JSON(http.StatusBadGateway, gin.H{"error": "ssh connectivity check failed", "detail": err.Error(), "status": access.Status, "checked_at": access.LastCheckedAt})
		return
	}
	h.audit.Record(actorID, actorEmail, "ssh.check", "server", c.Param("id"), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"status": access.Status, "checked_at": access.LastCheckedAt})
}

func (h Handler) serverMetrics(c *gin.Context) {
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(serverID)).Error) {
		return
	}
	var rows []models.MetricSample
	cutoff := time.Now().UTC().Add(-services.MetricRetention)
	h.db.Where("server_id = ? AND collected_at >= ?", c.Param("id"), cutoff).Order("collected_at desc").Limit(200).Find(&rows)
	if len(rows) == 0 {
		now := time.Now().UTC()
		rows = []models.MetricSample{{ServerID: uint(serverID), MetricName: "host_up", Value: 1, Unit: "bool", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "cpu_usage", Value: 38, Unit: "%", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "memory_usage", Value: 62, Unit: "%", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "disk_usage", Value: 54, Unit: "%", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "disk_smart_health", Value: 0, Unit: "bool", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "process_count", Value: 142, Unit: "count", CollectedAt: now}, {ServerID: uint(serverID), MetricName: "process_zombie_count", Value: 0, Unit: "count", CollectedAt: now}}
	}
	c.JSON(http.StatusOK, rows)
}

func (h Handler) startCollection(c *gin.Context) {
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(serverID)).Error) {
		return
	}
	if server.Status == "retired" || server.Status == "scrapped" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server is retired or scrapped"})
		return
	}
	_, email := middleware.Actor(c)
	job, err := h.collector.StartAgentlessCollection(uint(serverID), email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "monitor.collection.start", "server", c.Param("id"), "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, job)
}

func (h Handler) listCollections(c *gin.Context) {
	serverID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid server id"})
		return
	}
	var server models.Server
	if notFound(c, h.db.First(&server, uint(serverID)).Error) {
		return
	}
	var rows []models.CollectionJob
	h.db.Where("server_id = ?", c.Param("id")).Order("created_at desc").Limit(100).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listCollectionJobs(c *gin.Context) {
	query := h.db.Model(&models.CollectionJob{})
	if value := c.Query("server_id"); value != "" {
		query = query.Where("server_id = ?", value)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("mode"); value != "" {
		query = query.Where("mode = ?", value)
	}
	if value := c.Query("requested_by"); value != "" {
		query = query.Where("requested_by = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.CollectionJob
		query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.CollectionJob
	query.Order("created_at desc").Limit(100).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listLogEvents(c *gin.Context) {
	query := h.db.Model(&models.LogEvent{})
	if value := c.Query("server_id"); value != "" {
		query = query.Where("server_id = ?", value)
	}
	if value := c.Query("source"); value != "" {
		query = query.Where("source = ?", value)
	}
	if value := c.Query("level"); value != "" {
		query = query.Where("level = ?", value)
	}
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		query = query.Where("message LIKE ? OR trace_id LIKE ?", "%"+value+"%", "%"+value+"%")
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.LogEvent
		query.Order("occurred_at desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.LogEvent
	query.Order("occurred_at desc, id desc").Limit(200).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listAlerts(c *gin.Context) {
	query := h.db.Model(&models.Alert{})
	if value := c.Query("server_id"); value != "" {
		query = query.Where("server_id = ?", value)
	}
	if value := c.Query("severity"); value != "" {
		query = query.Where("severity = ?", value)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("rule_id"); value != "" {
		query = query.Where("rule_id = ?", value)
	}
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("title LIKE ? OR description LIKE ?", like, like)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.Alert
		query.Order("triggered_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.Alert
	query.Order("triggered_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) listAlertRules(c *gin.Context) {
	query := h.db.Model(&models.AlertRule{})
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("severity"); value != "" {
		query = query.Where("severity = ?", value)
	}
	if value := c.Query("metric_name"); value != "" {
		query = query.Where("metric_name = ?", value)
	}
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		query = query.Where("rule_id LIKE ? OR name LIKE ? OR description LIKE ?", "%"+value+"%", "%"+value+"%", "%"+value+"%")
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.AlertRule
		query.Order("created_at desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.AlertRule
	query.Order("created_at desc, id desc").Limit(200).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createAlertRule(c *gin.Context) {
	var req models.AlertRule
	if !bind(c, &req) {
		return
	}
	_, email := middleware.Actor(c)
	req.ID = 0
	req.CreatedBy = email
	req.CreatedAt = time.Time{}
	req.UpdatedAt = time.Time{}
	if err := validateAndNormalizeAlertRule(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&req).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "alert_rule.create", "alert_rule", req.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, req)
}

func (h Handler) updateAlertRule(c *gin.Context) {
	var row models.AlertRule
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
	updates, err := applyAlertRuleUpdate(&row, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Model(&row).Updates(updates).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.db.First(&row, row.ID)
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "alert_rule.update", "alert_rule", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func validateAndNormalizeAlertRule(rule *models.AlertRule) error {
	rule.RuleID = strings.TrimSpace(rule.RuleID)
	rule.Name = strings.TrimSpace(rule.Name)
	rule.MetricName = strings.TrimSpace(rule.MetricName)
	rule.Operator = strings.TrimSpace(rule.Operator)
	rule.Severity = strings.TrimSpace(rule.Severity)
	rule.Status = strings.TrimSpace(rule.Status)
	if rule.RuleID == "" || rule.Name == "" || rule.MetricName == "" {
		return errors.New("rule_id, name, and metric_name are required")
	}
	if !validAlertIdentifier(rule.RuleID) {
		return errors.New("rule_id contains invalid characters")
	}
	if !validAlertIdentifier(rule.MetricName) {
		return errors.New("metric_name contains invalid characters")
	}
	if rule.Operator == "" {
		rule.Operator = ">"
	}
	if rule.Severity == "" {
		rule.Severity = "warning"
	}
	if rule.Status == "" {
		rule.Status = "enabled"
	}
	if !stringIn(rule.Operator, ">", ">=", "<", "<=", "==") {
		return errors.New("operator must be one of >, >=, <, <=, ==")
	}
	if !stringIn(rule.Severity, "critical", "warning", "info") {
		return errors.New("severity must be one of critical, warning, info")
	}
	if !stringIn(rule.Status, "enabled", "disabled") {
		return errors.New("status must be one of enabled, disabled")
	}
	if math.IsNaN(rule.Threshold) || math.IsInf(rule.Threshold, 0) {
		return errors.New("threshold must be a finite number")
	}
	return nil
}

func applyAlertRuleUpdate(rule *models.AlertRule, req map[string]any) (map[string]any, error) {
	updates := map[string]any{}
	stringFields := []struct {
		key    string
		target *string
	}{
		{"rule_id", &rule.RuleID},
		{"name", &rule.Name},
		{"description", &rule.Description},
		{"metric_name", &rule.MetricName},
		{"operator", &rule.Operator},
		{"severity", &rule.Severity},
		{"status", &rule.Status},
	}
	for _, field := range stringFields {
		raw, ok := req[field.key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if !ok && raw != nil {
			return nil, errors.New(field.key + " must be a string")
		}
		*field.target = strings.TrimSpace(value)
		updates[field.key] = *field.target
	}
	if raw, ok := req["threshold"]; ok {
		value, err := floatFromUpdateValue(raw, "threshold")
		if err != nil {
			return nil, err
		}
		rule.Threshold = value
		updates["threshold"] = value
	}
	if err := validateAndNormalizeAlertRule(rule); err != nil {
		return nil, err
	}
	for key := range updates {
		switch key {
		case "rule_id":
			updates[key] = rule.RuleID
		case "name":
			updates[key] = rule.Name
		case "metric_name":
			updates[key] = rule.MetricName
		case "operator":
			updates[key] = rule.Operator
		case "severity":
			updates[key] = rule.Severity
		case "status":
			updates[key] = rule.Status
		}
	}
	return updates, nil
}

func floatFromUpdateValue(raw any, field string) (float64, error) {
	if raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case float64:
		return value, nil
	case int:
		return float64(value), nil
	default:
		return 0, errors.New(field + " must be a number")
	}
}

func validAlertIdentifier(value string) bool {
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (h Handler) evaluateAlertRules(c *gin.Context) {
	var rules []models.AlertRule
	h.db.Where("status = ?", "enabled").Find(&rules)
	now := time.Now().UTC()
	cutoff := now.Add(-services.MetricRetention)
	created := []models.Alert{}
	deduplicated := 0
	for _, rule := range rules {
		var samples []models.MetricSample
		h.db.Where("metric_name = ? AND collected_at >= ?", rule.MetricName, cutoff).Order("collected_at desc").Limit(200).Find(&samples)
		seen := map[uint]bool{}
		for _, sample := range samples {
			if seen[sample.ServerID] {
				continue
			}
			seen[sample.ServerID] = true
			if !compareMetric(sample.Value, rule.Operator, rule.Threshold) {
				continue
			}
			description := alertDescription(rule, sample)
			var existing models.Alert
			if err := h.db.Where("server_id = ? AND rule_id = ? AND status != ?", sample.ServerID, rule.RuleID, "resolved").First(&existing).Error; err == nil {
				deduplicated++
				h.db.Model(&existing).Updates(map[string]any{"severity": rule.Severity, "title": rule.Name, "description": description})
				continue
			}
			alert := models.Alert{ServerID: sample.ServerID, RuleID: rule.RuleID, Severity: rule.Severity, Status: "firing", Title: rule.Name, Description: description, TriggeredAt: now}
			if err := h.db.Create(&alert).Error; err == nil {
				h.db.Create(&models.AlertEvent{AlertID: alert.ID, Action: "trigger", ActorEmail: "system", Note: description, CreatedAt: now})
				created = append(created, alert)
			}
		}
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "alert_rule.evaluate", "alert_rule", len(created), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"created": len(created), "deduplicated": deduplicated, "alerts": created})
}

func alertDescription(rule models.AlertRule, sample models.MetricSample) string {
	return "metric " + rule.MetricName + " value " + strconv.FormatFloat(sample.Value, 'f', 2, 64) + " " + rule.Operator + " " + strconv.FormatFloat(rule.Threshold, 'f', 2, 64)
}

func compareMetric(value float64, operator string, threshold float64) bool {
	switch operator {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	default:
		return false
	}
}

func (h Handler) ackAlert(c *gin.Context) {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Note) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note must be 1000 characters or fewer"})
		return
	}
	now := time.Now().UTC()
	id, email := middleware.Actor(c)
	var alert models.Alert
	if notFound(c, h.db.First(&alert, c.Param("id")).Error) {
		return
	}
	if alert.Status == "resolved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "resolved alert cannot be acknowledged"})
		return
	}
	if alert.Status == "acknowledged" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alert is already acknowledged"})
		return
	}
	h.db.Model(&alert).Updates(map[string]any{"status": "acknowledged", "acknowledged_by": email, "acknowledged_at": now})
	h.db.Create(&models.AlertEvent{AlertID: uintFromParam(c.Param("id")), Action: "ack", ActorID: id, ActorEmail: email, Note: req.Note, CreatedAt: now})
	h.audit.Record(id, email, "alert.ack", "alert", c.Param("id"), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	h.db.First(&alert, c.Param("id"))
	c.JSON(http.StatusOK, alert)
}

func (h Handler) resolveAlert(c *gin.Context) {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Note) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note must be 1000 characters or fewer"})
		return
	}
	now := time.Now().UTC()
	id, email := middleware.Actor(c)
	var alert models.Alert
	if notFound(c, h.db.First(&alert, c.Param("id")).Error) {
		return
	}
	if alert.Status == "resolved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alert is already resolved"})
		return
	}
	h.db.Model(&alert).Updates(map[string]any{"status": "resolved", "resolved_by": email, "resolved_at": now})
	h.db.Create(&models.AlertEvent{AlertID: uintFromParam(c.Param("id")), Action: "resolve", ActorID: id, ActorEmail: email, Note: req.Note, CreatedAt: now})
	h.audit.Record(id, email, "alert.resolve", "alert", c.Param("id"), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	h.db.First(&alert, c.Param("id"))
	c.JSON(http.StatusOK, alert)
}

func (h Handler) listAlertEvents(c *gin.Context) {
	var rows []models.AlertEvent
	h.db.Where("alert_id = ?", c.Param("id")).Order("created_at desc, id desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}
