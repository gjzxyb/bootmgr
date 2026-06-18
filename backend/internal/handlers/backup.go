package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const backupVersion = "mvp-v0.1"

type backupPayload struct {
	GeneratedAt       time.Time                    `json:"generated_at"`
	Version           string                       `json:"version"`
	Users             []models.User                `json:"users"`
	Tenants           []models.Tenant              `json:"tenants"`
	NetworkConfigs    []models.NetworkConfig       `json:"network_configs"`
	Servers           []models.Server              `json:"servers"`
	Hardware          []models.HardwareInventory   `json:"hardware_inventories"`
	StatusHistory     []models.ServerStatusHistory `json:"server_status_history"`
	RetirementRecords []models.RetirementRecord    `json:"retirement_records"`
	Images            []models.Image               `json:"images"`
	InstallTemplates  []models.InstallTemplate     `json:"install_templates"`
	WorkflowTemplates []models.WorkflowTemplate    `json:"workflow_templates"`
	Deployments       []models.Deployment          `json:"deployments"`
	WorkflowRuns      []models.WorkflowRun         `json:"workflow_runs"`
	TaskExecutions    []models.TaskExecution       `json:"task_executions"`
	Metrics           []models.MetricSample        `json:"metric_samples"`
	LogEvents         []models.LogEvent            `json:"log_events"`
	CollectionJobs    []models.CollectionJob       `json:"collection_jobs"`
	ScriptJobs        []models.ScriptJob           `json:"script_jobs"`
	ScriptExecutions  []models.ScriptExecution     `json:"script_executions"`
	TerminalSessions  []models.TerminalSession     `json:"terminal_sessions"`
	Alerts            []models.Alert               `json:"alerts"`
	AlertEvents       []models.AlertEvent          `json:"alert_events"`
	AlertRules        []models.AlertRule           `json:"alert_rules"`
	AuditLogs         []models.AuditLog            `json:"audit_logs"`
}

type backupValidationCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type backupValidationReport struct {
	Status       string                  `json:"status"`
	Version      string                  `json:"version"`
	GeneratedAt  *time.Time              `json:"generated_at,omitempty"`
	Totals       map[string]int          `json:"totals"`
	TargetCounts map[string]int64        `json:"target_counts"`
	Checks       []backupValidationCheck `json:"checks"`
}

type backupRestoreResult struct {
	Status   string                  `json:"status"`
	Imported map[string]int          `json:"imported"`
	Warnings []backupValidationCheck `json:"warnings,omitempty"`
}

func (h Handler) exportBackup(c *gin.Context) {
	payload := backupPayload{GeneratedAt: time.Now().UTC(), Version: backupVersion}
	queries := []struct {
		name string
		dst  any
	}{
		{"users", &payload.Users},
		{"tenants", &payload.Tenants},
		{"network_configs", &payload.NetworkConfigs},
		{"servers", &payload.Servers},
		{"hardware_inventories", &payload.Hardware},
		{"server_status_history", &payload.StatusHistory},
		{"retirement_records", &payload.RetirementRecords},
		{"images", &payload.Images},
		{"install_templates", &payload.InstallTemplates},
		{"workflow_templates", &payload.WorkflowTemplates},
		{"deployments", &payload.Deployments},
		{"workflow_runs", &payload.WorkflowRuns},
		{"task_executions", &payload.TaskExecutions},
		{"metric_samples", &payload.Metrics},
		{"log_events", &payload.LogEvents},
		{"collection_jobs", &payload.CollectionJobs},
		{"script_jobs", &payload.ScriptJobs},
		{"script_executions", &payload.ScriptExecutions},
		{"terminal_sessions", &payload.TerminalSessions},
		{"alerts", &payload.Alerts},
		{"alert_events", &payload.AlertEvents},
		{"alert_rules", &payload.AlertRules},
		{"audit_logs", &payload.AuditLogs},
	}
	for _, query := range queries {
		if err := h.db.Find(query.dst).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("backup query %s: %v", query.name, err)})
			return
		}
	}

	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "ops.backup.export", "backup", payload.GeneratedAt.Format(time.RFC3339), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.Header("Content-Disposition", "attachment; filename=\"baremetal-backup-"+payload.GeneratedAt.Format("20060102-150405")+".json\"")
	c.JSON(http.StatusOK, payload)
}

func (h Handler) validateBackup(c *gin.Context) {
	payload, raw, err := readBackupPayload(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report := h.buildBackupValidationReport(payload, raw)
	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "ops.backup.validate", "backup", report.Version, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, report)
}

func (h Handler) restoreBackup(c *gin.Context) {
	payload, raw, err := readBackupPayload(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	recoveryAdmin, err := h.restoreRecoveryAdmin(actorID, actorEmail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load recovery admin: " + err.Error()})
		return
	}
	report := h.buildBackupValidationReport(payload, raw)
	if report.Status == "error" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup validation failed", "report": report})
		return
	}
	if err := h.ensureFreshRestoreTarget(report.TargetCounts); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "report": report})
		return
	}
	if err := h.restoreBackupPayload(payload, recoveryAdmin); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "restore backup: " + err.Error()})
		return
	}
	h.audit.Record(actorID, actorEmail, "ops.backup.restore", "backup", payload.GeneratedAt.Format(time.RFC3339), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, backupRestoreResult{Status: "restored", Imported: backupTotals(payload), Warnings: backupWarnings(report)})
}

func readBackupPayload(c *gin.Context) (backupPayload, map[string]json.RawMessage, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 50<<20)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return backupPayload{}, nil, fmt.Errorf("read backup payload: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return backupPayload{}, nil, fmt.Errorf("invalid backup JSON: %w", err)
	}
	var payload backupPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return backupPayload{}, nil, fmt.Errorf("invalid backup schema: %w", err)
	}
	return payload, raw, nil
}

func (h Handler) buildBackupValidationReport(payload backupPayload, raw map[string]json.RawMessage) backupValidationReport {
	report := backupValidationReport{
		Status:       "ok",
		Version:      payload.Version,
		Totals:       backupTotals(payload),
		TargetCounts: map[string]int64{},
		Checks:       []backupValidationCheck{},
	}
	if !payload.GeneratedAt.IsZero() {
		generatedAt := payload.GeneratedAt
		report.GeneratedAt = &generatedAt
	}

	requiredSections := []string{"generated_at", "version", "users", "tenants", "network_configs", "servers", "hardware_inventories", "server_status_history", "images", "install_templates", "workflow_templates", "deployments", "workflow_runs", "task_executions", "metric_samples", "log_events", "collection_jobs", "script_jobs", "script_executions", "terminal_sessions", "alerts", "alert_events", "alert_rules", "audit_logs"}
	missing := []string{}
	for _, section := range requiredSections {
		if _, ok := raw[section]; !ok {
			missing = append(missing, section)
		}
	}
	if len(missing) > 0 {
		report.addCheck("schema", "error", fmt.Sprintf("missing sections: %v", missing))
	} else {
		report.addCheck("schema", "ok", "all expected sections are present")
	}
	if _, ok := raw["retirement_records"]; !ok {
		report.addCheck("retirement_records", "warning", "backup does not contain retirement_records; restored retired assets may lack erasure evidence")
	} else {
		report.addCheck("retirement_records", "ok", "backup contains retirement_records section")
	}

	if payload.Version != backupVersion {
		report.addCheck("version", "error", fmt.Sprintf("unsupported backup version %q; expected %s", payload.Version, backupVersion))
	} else {
		report.addCheck("version", "ok", "backup version is supported")
	}

	if payload.GeneratedAt.IsZero() {
		report.addCheck("generated_at", "warning", "backup generated_at is missing or invalid")
	} else if payload.GeneratedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		report.addCheck("generated_at", "warning", "backup generated_at is in the future")
	} else {
		report.addCheck("generated_at", "ok", "backup timestamp is valid")
	}

	admins := 0
	for _, user := range payload.Users {
		if user.Role == "admin" {
			admins++
		}
	}
	if admins == 0 {
		report.addCheck("users", "warning", "backup does not contain an admin user")
	} else {
		report.addCheck("users", "ok", fmt.Sprintf("backup contains %d admin user(s)", admins))
	}
	if len(payload.Users) > 0 {
		report.addCheck("user_passwords", "warning", "user password hashes are intentionally omitted; restored users require password reset")
	}

	if contentProblems := backupContentProblems(payload); len(contentProblems) > 0 {
		report.addCheck("content", "error", fmt.Sprintf("backup content problems: %v", contentProblems))
	} else {
		report.addCheck("content", "ok", "backup rows pass format and uniqueness checks")
	}

	if missingRefs := backupReferenceProblems(payload); len(missingRefs) > 0 {
		report.addCheck("references", "error", fmt.Sprintf("referential integrity problems: %v", missingRefs))
	} else {
		report.addCheck("references", "ok", "backup references are internally consistent")
	}

	if deploymentNetworks := countDeploymentNetworks(payload.NetworkConfigs); deploymentNetworks == 0 {
		report.addCheck("network_configs", "warning", "backup has no enabled deployment network")
	} else {
		report.addCheck("network_configs", "ok", fmt.Sprintf("backup has %d enabled deployment network(s)", deploymentNetworks))
	}

	targetCounts, err := h.backupTargetCounts()
	if err != nil {
		report.addCheck("target_database", "warning", "could not inspect target database: "+err.Error())
	} else {
		report.TargetCounts = targetCounts
		totalRows := int64(0)
		for _, count := range targetCounts {
			totalRows += count
		}
		if totalRows > 0 {
			report.addCheck("target_database", "warning", "target database is not empty; restore should use an empty database or an explicit overwrite workflow")
		} else {
			report.addCheck("target_database", "ok", "target database appears empty")
		}
	}

	if len(payload.AlertRules) == 0 {
		report.addCheck("alert_rules", "warning", "backup does not contain alert rules")
	} else {
		report.addCheck("alert_rules", "ok", fmt.Sprintf("backup contains %d alert rule(s)", len(payload.AlertRules)))
	}
	return report
}

func (r *backupValidationReport) addCheck(name string, status string, message string) {
	if status == "error" {
		r.Status = "error"
	} else if status == "warning" && r.Status != "error" {
		r.Status = "warning"
	}
	r.Checks = append(r.Checks, backupValidationCheck{Name: name, Status: status, Message: message})
}

func backupTotals(payload backupPayload) map[string]int {
	return map[string]int{
		"users":                 len(payload.Users),
		"tenants":               len(payload.Tenants),
		"network_configs":       len(payload.NetworkConfigs),
		"servers":               len(payload.Servers),
		"hardware_inventories":  len(payload.Hardware),
		"server_status_history": len(payload.StatusHistory),
		"retirement_records":    len(payload.RetirementRecords),
		"images":                len(payload.Images),
		"install_templates":     len(payload.InstallTemplates),
		"workflow_templates":    len(payload.WorkflowTemplates),
		"deployments":           len(payload.Deployments),
		"workflow_runs":         len(payload.WorkflowRuns),
		"task_executions":       len(payload.TaskExecutions),
		"metric_samples":        len(payload.Metrics),
		"log_events":            len(payload.LogEvents),
		"collection_jobs":       len(payload.CollectionJobs),
		"script_jobs":           len(payload.ScriptJobs),
		"script_executions":     len(payload.ScriptExecutions),
		"terminal_sessions":     len(payload.TerminalSessions),
		"alerts":                len(payload.Alerts),
		"alert_events":          len(payload.AlertEvents),
		"alert_rules":           len(payload.AlertRules),
		"audit_logs":            len(payload.AuditLogs),
	}
}

func (h Handler) backupTargetCounts() (map[string]int64, error) {
	queries := []struct {
		name  string
		model any
	}{
		{"users", &models.User{}},
		{"credentials", &models.Credential{}},
		{"ssh_accesses", &models.SSHAccess{}},
		{"tenants", &models.Tenant{}},
		{"network_configs", &models.NetworkConfig{}},
		{"servers", &models.Server{}},
		{"bmc_endpoints", &models.BmcEndpoint{}},
		{"hardware_inventories", &models.HardwareInventory{}},
		{"server_status_history", &models.ServerStatusHistory{}},
		{"retirement_records", &models.RetirementRecord{}},
		{"images", &models.Image{}},
		{"install_templates", &models.InstallTemplate{}},
		{"workflow_templates", &models.WorkflowTemplate{}},
		{"deployments", &models.Deployment{}},
		{"boot_events", &models.BootEvent{}},
		{"metadata_tokens", &models.MetadataToken{}},
		{"workflow_runs", &models.WorkflowRun{}},
		{"task_executions", &models.TaskExecution{}},
		{"metric_samples", &models.MetricSample{}},
		{"log_events", &models.LogEvent{}},
		{"collection_jobs", &models.CollectionJob{}},
		{"script_jobs", &models.ScriptJob{}},
		{"script_executions", &models.ScriptExecution{}},
		{"terminal_sessions", &models.TerminalSession{}},
		{"alerts", &models.Alert{}},
		{"alert_events", &models.AlertEvent{}},
		{"alert_rules", &models.AlertRule{}},
		{"audit_logs", &models.AuditLog{}},
	}
	counts := make(map[string]int64, len(queries))
	for _, query := range queries {
		var count int64
		if err := h.db.Model(query.model).Count(&count).Error; err != nil {
			return counts, fmt.Errorf("%s: %w", query.name, err)
		}
		counts[query.name] = count
	}
	return counts, nil
}

func (h Handler) ensureFreshRestoreTarget(counts map[string]int64) error {
	if counts == nil {
		var err error
		counts, err = h.backupTargetCounts()
		if err != nil {
			return err
		}
	}
	for name, count := range counts {
		switch name {
		case "users":
			if count > 1 {
				return fmt.Errorf("target database has %d users; restore requires a fresh target with only the bootstrap admin user", count)
			}
		case "audit_logs":
			continue
		default:
			if count > 0 {
				return fmt.Errorf("target database is not empty: %s has %d row(s)", name, count)
			}
		}
	}
	return nil
}

func (h Handler) restoreBackupPayload(payload backupPayload, recoveryAdmin *models.User) error {
	users, err := prepareRestoredUsers(payload.Users, recoveryAdmin)
	if err != nil {
		return err
	}
	payload.Users = users
	return h.db.Transaction(func(tx *gorm.DB) error {
		if err := clearBackupTables(tx); err != nil {
			return err
		}
		inserts := []struct {
			name string
			rows any
			len  int
		}{
			{"users", &payload.Users, len(payload.Users)},
			{"tenants", &payload.Tenants, len(payload.Tenants)},
			{"network_configs", &payload.NetworkConfigs, len(payload.NetworkConfigs)},
			{"servers", &payload.Servers, len(payload.Servers)},
			{"hardware_inventories", &payload.Hardware, len(payload.Hardware)},
			{"server_status_history", &payload.StatusHistory, len(payload.StatusHistory)},
			{"retirement_records", &payload.RetirementRecords, len(payload.RetirementRecords)},
			{"images", &payload.Images, len(payload.Images)},
			{"install_templates", &payload.InstallTemplates, len(payload.InstallTemplates)},
			{"workflow_templates", &payload.WorkflowTemplates, len(payload.WorkflowTemplates)},
			{"deployments", &payload.Deployments, len(payload.Deployments)},
			{"workflow_runs", &payload.WorkflowRuns, len(payload.WorkflowRuns)},
			{"task_executions", &payload.TaskExecutions, len(payload.TaskExecutions)},
			{"metric_samples", &payload.Metrics, len(payload.Metrics)},
			{"log_events", &payload.LogEvents, len(payload.LogEvents)},
			{"collection_jobs", &payload.CollectionJobs, len(payload.CollectionJobs)},
			{"script_jobs", &payload.ScriptJobs, len(payload.ScriptJobs)},
			{"script_executions", &payload.ScriptExecutions, len(payload.ScriptExecutions)},
			{"terminal_sessions", &payload.TerminalSessions, len(payload.TerminalSessions)},
			{"alerts", &payload.Alerts, len(payload.Alerts)},
			{"alert_events", &payload.AlertEvents, len(payload.AlertEvents)},
			{"alert_rules", &payload.AlertRules, len(payload.AlertRules)},
			{"audit_logs", &payload.AuditLogs, len(payload.AuditLogs)},
		}
		for _, insert := range inserts {
			if insert.len == 0 {
				continue
			}
			if err := tx.Create(insert.rows).Error; err != nil {
				return fmt.Errorf("insert %s: %w", insert.name, err)
			}
		}
		if err := resetBackupTableSequences(tx); err != nil {
			return fmt.Errorf("reset identity sequences: %w", err)
		}
		return nil
	})
}

func (h Handler) restoreRecoveryAdmin(actorID uint, actorEmail string) (*models.User, error) {
	var user models.User
	var err error
	if actorID != 0 {
		err = h.db.First(&user, actorID).Error
	} else if actorEmail != "" {
		err = h.db.Where("email = ?", actorEmail).First(&user).Error
	} else {
		return nil, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if user.Role != "admin" {
		return nil, nil
	}
	return &user, nil
}

func clearBackupTables(tx *gorm.DB) error {
	modelsToClear := []struct {
		name  string
		model any
	}{
		{"credentials", &models.Credential{}},
		{"ssh_accesses", &models.SSHAccess{}},
		{"bmc_endpoints", &models.BmcEndpoint{}},
		{"metadata_tokens", &models.MetadataToken{}},
		{"boot_events", &models.BootEvent{}},
		{"alert_events", &models.AlertEvent{}},
		{"alerts", &models.Alert{}},
		{"terminal_sessions", &models.TerminalSession{}},
		{"script_executions", &models.ScriptExecution{}},
		{"script_jobs", &models.ScriptJob{}},
		{"collection_jobs", &models.CollectionJob{}},
		{"log_events", &models.LogEvent{}},
		{"metric_samples", &models.MetricSample{}},
		{"task_executions", &models.TaskExecution{}},
		{"workflow_runs", &models.WorkflowRun{}},
		{"deployments", &models.Deployment{}},
		{"workflow_templates", &models.WorkflowTemplate{}},
		{"install_templates", &models.InstallTemplate{}},
		{"images", &models.Image{}},
		{"retirement_records", &models.RetirementRecord{}},
		{"server_status_history", &models.ServerStatusHistory{}},
		{"hardware_inventories", &models.HardwareInventory{}},
		{"servers", &models.Server{}},
		{"network_configs", &models.NetworkConfig{}},
		{"tenants", &models.Tenant{}},
		{"alert_rules", &models.AlertRule{}},
		{"audit_logs", &models.AuditLog{}},
		{"users", &models.User{}},
	}
	for _, item := range modelsToClear {
		if err := tx.Unscoped().Where("1 = 1").Delete(item.model).Error; err != nil {
			return fmt.Errorf("clear %s: %w", item.name, err)
		}
	}
	return nil
}

func resetBackupTableSequences(tx *gorm.DB) error {
	tables := []string{
		"users",
		"tenants",
		"network_configs",
		"servers",
		"bmc_endpoints",
		"credentials",
		"ssh_accesses",
		"hardware_inventories",
		"server_status_histories",
		"retirement_records",
		"images",
		"install_templates",
		"workflow_templates",
		"deployments",
		"boot_events",
		"metadata_tokens",
		"workflow_runs",
		"task_executions",
		"metric_samples",
		"log_events",
		"collection_jobs",
		"script_jobs",
		"script_executions",
		"terminal_sessions",
		"alerts",
		"alert_rules",
		"alert_events",
		"audit_logs",
	}
	switch tx.Dialector.Name() {
	case "postgres":
		for _, table := range tables {
			query := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM "%s"), 0) + 1, false)`, table, table)
			if err := tx.Exec(query).Error; err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
		}
	case "sqlite":
		var sequenceTableCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'`).Scan(&sequenceTableCount).Error; err != nil {
			return err
		}
		if sequenceTableCount == 0 {
			return nil
		}
		for _, table := range tables {
			var maxID int64
			if err := tx.Table(table).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
				return fmt.Errorf("%s max id: %w", table, err)
			}
			update := tx.Exec(`UPDATE sqlite_sequence SET seq = ? WHERE name = ?`, maxID, table)
			if update.Error != nil {
				return fmt.Errorf("%s: %w", table, update.Error)
			}
			if update.RowsAffected == 0 {
				if err := tx.Exec(`INSERT INTO sqlite_sequence(name, seq) VALUES (?, ?)`, table, maxID).Error; err != nil {
					return fmt.Errorf("%s: %w", table, err)
				}
			}
		}
	}
	return nil
}

func prepareRestoredUsers(users []models.User, recoveryAdmin *models.User) ([]models.User, error) {
	if len(users) == 0 {
		if recoveryAdmin == nil {
			return users, nil
		}
		return []models.User{recoveryAdminUser(*recoveryAdmin, 1)}, nil
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("restore-disabled-"+hex.EncodeToString(secret)), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	maxID := uint(0)
	recoveryMatched := false
	for i := range users {
		if users[i].ID > maxID {
			maxID = users[i].ID
		}
		users[i].PasswordHash = string(hash)
		if recoveryAdmin != nil && strings.EqualFold(users[i].Email, recoveryAdmin.Email) {
			users[i].Role = "admin"
			users[i].PasswordHash = recoveryAdmin.PasswordHash
			recoveryMatched = true
		}
	}
	if recoveryAdmin != nil && !recoveryMatched {
		users = append(users, recoveryAdminUser(*recoveryAdmin, maxID+1))
	}
	return users, nil
}

func recoveryAdminUser(user models.User, id uint) models.User {
	now := time.Now().UTC()
	user.ID = id
	user.Role = "admin"
	if strings.TrimSpace(user.Name) == "" {
		user.Name = "Recovery Admin"
	}
	user.CreatedAt = now
	user.UpdatedAt = now
	user.DeletedAt = gorm.DeletedAt{}
	return user
}

func backupWarnings(report backupValidationReport) []backupValidationCheck {
	warnings := []backupValidationCheck{}
	for _, check := range report.Checks {
		if check.Status == "warning" {
			warnings = append(warnings, check)
		}
	}
	return warnings
}

func backupReferenceProblems(payload backupPayload) []string {
	serverIDs := make(map[uint]bool, len(payload.Servers))
	tenantIDs := make(map[string]bool, len(payload.Tenants))
	networkIDs := make(map[uint]bool, len(payload.NetworkConfigs))
	imageIDs := make(map[uint]bool, len(payload.Images))
	installTemplateIDs := make(map[uint]bool, len(payload.InstallTemplates))
	workflowTemplateIDs := make(map[uint]bool, len(payload.WorkflowTemplates))
	deploymentIDs := make(map[uint]bool, len(payload.Deployments))
	workflowRunIDs := make(map[uint]bool, len(payload.WorkflowRuns))
	scriptJobIDs := make(map[uint]bool, len(payload.ScriptJobs))
	alertIDs := make(map[uint]bool, len(payload.Alerts))
	for _, server := range payload.Servers {
		serverIDs[server.ID] = true
	}
	for _, tenant := range payload.Tenants {
		tenantIDs[tenant.TenantID] = true
	}
	for _, network := range payload.NetworkConfigs {
		networkIDs[network.ID] = true
	}
	for _, image := range payload.Images {
		imageIDs[image.ID] = true
	}
	for _, template := range payload.InstallTemplates {
		installTemplateIDs[template.ID] = true
	}
	for _, template := range payload.WorkflowTemplates {
		workflowTemplateIDs[template.ID] = true
	}
	for _, deployment := range payload.Deployments {
		deploymentIDs[deployment.ID] = true
	}
	for _, run := range payload.WorkflowRuns {
		workflowRunIDs[run.ID] = true
	}
	for _, job := range payload.ScriptJobs {
		scriptJobIDs[job.ID] = true
	}
	for _, alert := range payload.Alerts {
		alertIDs[alert.ID] = true
	}

	problems := []string{}
	for _, server := range payload.Servers {
		if server.TenantID != "" && !tenantIDs[server.TenantID] {
			problems = append(problems, fmt.Sprintf("server %d references missing tenant %s", server.ID, server.TenantID))
		}
	}
	for _, inventory := range payload.Hardware {
		if !serverIDs[inventory.ServerID] {
			problems = append(problems, fmt.Sprintf("hardware_inventory %d references missing server %d", inventory.ID, inventory.ServerID))
		}
	}
	for _, history := range payload.StatusHistory {
		if !serverIDs[history.ServerID] {
			problems = append(problems, fmt.Sprintf("server_status_history %d references missing server %d", history.ID, history.ServerID))
		}
	}
	for _, record := range payload.RetirementRecords {
		if !serverIDs[record.ServerID] {
			problems = append(problems, fmt.Sprintf("retirement_record %d references missing server %d", record.ID, record.ServerID))
		}
	}
	for _, deployment := range payload.Deployments {
		if !serverIDs[deployment.ServerID] {
			problems = append(problems, fmt.Sprintf("deployment %d references missing server %d", deployment.ID, deployment.ServerID))
		}
		if !imageIDs[deployment.ImageID] {
			problems = append(problems, fmt.Sprintf("deployment %d references missing image %d", deployment.ID, deployment.ImageID))
		}
		if deployment.TemplateID != nil && !installTemplateIDs[*deployment.TemplateID] {
			problems = append(problems, fmt.Sprintf("deployment %d references missing install_template %d", deployment.ID, *deployment.TemplateID))
		}
		if deployment.WorkflowID != nil && !workflowTemplateIDs[*deployment.WorkflowID] {
			problems = append(problems, fmt.Sprintf("deployment %d references missing workflow_template %d", deployment.ID, *deployment.WorkflowID))
		}
		if deployment.NetworkID != nil && !networkIDs[*deployment.NetworkID] {
			problems = append(problems, fmt.Sprintf("deployment %d references missing network_config %d", deployment.ID, *deployment.NetworkID))
		}
	}
	for _, run := range payload.WorkflowRuns {
		if !deploymentIDs[run.DeploymentID] {
			problems = append(problems, fmt.Sprintf("workflow_run %d references missing deployment %d", run.ID, run.DeploymentID))
		}
	}
	for _, task := range payload.TaskExecutions {
		if !workflowRunIDs[task.WorkflowRunID] {
			problems = append(problems, fmt.Sprintf("task_execution %d references missing workflow_run %d", task.ID, task.WorkflowRunID))
		}
	}
	for _, metric := range payload.Metrics {
		if !serverIDs[metric.ServerID] {
			problems = append(problems, fmt.Sprintf("metric_sample %d references missing server %d", metric.ID, metric.ServerID))
		}
	}
	for _, event := range payload.LogEvents {
		if event.ServerID != 0 && !serverIDs[event.ServerID] {
			problems = append(problems, fmt.Sprintf("log_event %d references missing server %d", event.ID, event.ServerID))
		}
	}
	for _, job := range payload.CollectionJobs {
		if !serverIDs[job.ServerID] {
			problems = append(problems, fmt.Sprintf("collection_job %d references missing server %d", job.ID, job.ServerID))
		}
	}
	for _, execution := range payload.ScriptExecutions {
		if !scriptJobIDs[execution.ScriptJobID] {
			problems = append(problems, fmt.Sprintf("script_execution %d references missing script_job %d", execution.ID, execution.ScriptJobID))
		}
		if !serverIDs[execution.ServerID] {
			problems = append(problems, fmt.Sprintf("script_execution %d references missing server %d", execution.ID, execution.ServerID))
		}
	}
	for _, session := range payload.TerminalSessions {
		if !serverIDs[session.ServerID] {
			problems = append(problems, fmt.Sprintf("terminal_session %d references missing server %d", session.ID, session.ServerID))
		}
	}
	for _, alert := range payload.Alerts {
		if alert.ServerID != 0 && !serverIDs[alert.ServerID] {
			problems = append(problems, fmt.Sprintf("alert %d references missing server %d", alert.ID, alert.ServerID))
		}
	}
	for _, event := range payload.AlertEvents {
		if !alertIDs[event.AlertID] {
			problems = append(problems, fmt.Sprintf("alert_event %d references missing alert %d", event.ID, event.AlertID))
		}
	}
	return problems
}

func backupContentProblems(payload backupPayload) []string {
	problems := []string{}
	problems = append(problems, backupUserProblems(payload.Users)...)
	problems = append(problems, backupTenantProblems(payload.Tenants)...)
	problems = append(problems, backupServerProblems(payload.Servers)...)
	problems = append(problems, backupRetirementRecordProblems(payload.RetirementRecords)...)
	problems = append(problems, backupTenantQuotaUsageProblems(payload.Tenants, payload.Servers)...)
	problems = append(problems, backupNetworkConfigProblems(payload.NetworkConfigs)...)
	problems = append(problems, backupAlertRuleProblems(payload.AlertRules)...)
	return problems
}

func backupUserProblems(users []models.User) []string {
	problems := duplicateIDProblems("users", users, func(user models.User) uint { return user.ID })
	problems = append(problems, duplicateStringProblems("users.email", users, func(user models.User) string { return user.Email })...)
	for i, user := range users {
		email := normalizeEmail(user.Email)
		if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
			problems = append(problems, fmt.Sprintf("user index %d has invalid email %q", i, user.Email))
		}
		if strings.TrimSpace(user.Name) == "" {
			problems = append(problems, fmt.Sprintf("user index %d has empty name", i))
		}
		if !validRole(strings.TrimSpace(user.Role)) {
			problems = append(problems, fmt.Sprintf("user index %d has invalid role %q", i, user.Role))
		}
	}
	return problems
}

func backupTenantProblems(tenants []models.Tenant) []string {
	problems := duplicateIDProblems("tenants", tenants, func(tenant models.Tenant) uint { return tenant.ID })
	problems = append(problems, duplicateStringProblems("tenants.tenant_id", tenants, func(tenant models.Tenant) string { return tenant.TenantID })...)
	for i, tenant := range tenants {
		copy := tenant
		if err := normalizeTenant(&copy); err != nil {
			problems = append(problems, fmt.Sprintf("tenant index %d (%q): %v", i, tenant.TenantID, err))
		}
	}
	return problems
}

func backupServerProblems(servers []models.Server) []string {
	problems := duplicateIDProblems("servers", servers, func(server models.Server) uint { return server.ID })
	normalized := make([]models.Server, 0, len(servers))
	for i, server := range servers {
		copy := server
		if err := normalizeServer(&copy); err != nil {
			problems = append(problems, fmt.Sprintf("server index %d (id %d): %v", i, server.ID, err))
			continue
		}
		normalized = append(normalized, copy)
	}
	problems = append(problems, duplicateStringProblems("servers.asset_no", normalized, func(server models.Server) string { return server.AssetNo })...)
	problems = append(problems, duplicateStringProblems("servers.hostname", normalized, func(server models.Server) string { return server.Hostname })...)
	problems = append(problems, duplicateStringProblems("servers.primary_mac", normalized, func(server models.Server) string { return server.PrimaryMAC })...)
	return problems
}

func backupRetirementRecordProblems(records []models.RetirementRecord) []string {
	problems := duplicateIDProblems("retirement_records", records, func(record models.RetirementRecord) uint { return record.ID })
	for i, record := range records {
		copy := record
		if err := normalizeRetirementRecord(&copy); err != nil {
			problems = append(problems, fmt.Sprintf("retirement_record index %d (id %d): %v", i, record.ID, err))
		}
	}
	return problems
}

func backupTenantQuotaUsageProblems(tenants []models.Tenant, servers []models.Server) []string {
	problems := []string{}
	usage := map[string]int64{}
	for _, server := range servers {
		tenantID := strings.TrimSpace(server.TenantID)
		if tenantID != "" {
			usage[tenantID]++
		}
	}
	for _, tenant := range tenants {
		limit, ok, err := tenantServerQuotaLimit(tenant)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if ok && usage[tenant.TenantID] > limit {
			problems = append(problems, fmt.Sprintf("tenant %q server quota exceeded in backup: limit %d, current %d", tenant.TenantID, limit, usage[tenant.TenantID]))
		}
	}
	return problems
}

func backupNetworkConfigProblems(networks []models.NetworkConfig) []string {
	type enabledNetwork struct {
		id     uint
		name   string
		prefix netip.Prefix
	}

	problems := duplicateIDProblems("network_configs", networks, func(network models.NetworkConfig) uint { return network.ID })
	enabled := map[string][]enabledNetwork{}
	for i, network := range networks {
		copy := network
		if err := validateAndNormalizeNetworkConfig(&copy); err != nil {
			problems = append(problems, fmt.Sprintf("network_config index %d (id %d): %v", i, network.ID, err))
			continue
		}
		if copy.Status != "enabled" {
			continue
		}
		prefix, err := netip.ParsePrefix(copy.CIDR)
		if err != nil {
			problems = append(problems, fmt.Sprintf("network_config index %d (id %d): cidr must be a valid CIDR", i, network.ID))
			continue
		}
		prefix = prefix.Masked()
		for _, existing := range enabled[copy.Purpose] {
			if prefix.Overlaps(existing.prefix) {
				problems = append(problems, fmt.Sprintf("network_config %d (%q) cidr %s overlaps with network_config %d (%q) cidr %s for purpose %s", copy.ID, copy.Name, prefix.String(), existing.id, existing.name, existing.prefix.String(), copy.Purpose))
			}
		}
		enabled[copy.Purpose] = append(enabled[copy.Purpose], enabledNetwork{id: copy.ID, name: copy.Name, prefix: prefix})
	}
	return problems
}

func backupAlertRuleProblems(rules []models.AlertRule) []string {
	problems := duplicateIDProblems("alert_rules", rules, func(rule models.AlertRule) uint { return rule.ID })
	problems = append(problems, duplicateStringProblems("alert_rules.rule_id", rules, func(rule models.AlertRule) string { return rule.RuleID })...)
	for i, rule := range rules {
		copy := rule
		if err := validateAndNormalizeAlertRule(&copy); err != nil {
			problems = append(problems, fmt.Sprintf("alert_rule index %d (%q): %v", i, rule.RuleID, err))
		}
	}
	return problems
}

func duplicateIDProblems[T any](section string, rows []T, id func(T) uint) []string {
	problems := []string{}
	seen := map[uint]struct{}{}
	for _, row := range rows {
		value := id(row)
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			problems = append(problems, fmt.Sprintf("%s contains duplicate id %d", section, value))
			continue
		}
		seen[value] = struct{}{}
	}
	return problems
}

func duplicateStringProblems[T any](field string, rows []T, key func(T) string) []string {
	problems := []string{}
	seen := map[string]string{}
	for _, row := range rows {
		raw := strings.TrimSpace(key(row))
		if raw == "" {
			continue
		}
		normalized := strings.ToLower(raw)
		if previous, ok := seen[normalized]; ok {
			problems = append(problems, fmt.Sprintf("%s contains duplicate value %q (matches %q)", field, raw, previous))
			continue
		}
		seen[normalized] = raw
	}
	return problems
}

func countDeploymentNetworks(networks []models.NetworkConfig) int {
	count := 0
	for _, network := range networks {
		if network.Purpose == "deployment" && network.Status == "enabled" {
			count++
		}
	}
	return count
}
