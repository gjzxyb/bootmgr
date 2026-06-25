package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type deploymentConflictError string

func (e deploymentConflictError) Error() string { return string(e) }

const defaultDeploymentErasePolicy = "quick"
const maxBatchDeployments = 20

type deploymentCreatePayload struct {
	ImageID        uint           `json:"image_id" binding:"required"`
	TemplateID     *uint          `json:"template_id"`
	WorkflowID     *uint          `json:"workflow_id"`
	NetworkID      *uint          `json:"network_id"`
	Variables      datatypes.JSON `json:"variables"`
	ErasePolicy    string         `json:"erase_policy"`
	EraseConfirmed bool           `json:"erase_confirmed"`
}

type deploymentPreflightPayload struct {
	ServerID  uint   `json:"server_id"`
	ServerIDs []uint `json:"server_ids"`
	deploymentCreatePayload
}

type deploymentPreflightCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type deploymentPreflightTarget struct {
	ServerID        uint                       `json:"server_id"`
	Status          string                     `json:"status"`
	PowerControl    string                     `json:"power_control"`
	Checks          []deploymentPreflightCheck `json:"checks"`
	Problems        []string                   `json:"problems"`
	Warnings        []string                   `json:"warnings"`
	OperatorActions []string                   `json:"operator_actions"`
}

type deploymentPreflightResponse struct {
	Status   string                      `json:"status"`
	Targets  []deploymentPreflightTarget `json:"targets"`
	Problems []string                    `json:"problems"`
	Warnings []string                    `json:"warnings"`
}

type deploymentCreateParams struct {
	ServerID         uint
	ImageID          uint
	TemplateID       *uint
	WorkflowID       *uint
	NetworkID        *uint
	Variables        datatypes.JSON
	ErasePolicy      string
	EraseConfirmedAt time.Time
	RequestedBy      string
}

func normalizeDeploymentErasePolicy(policy string) (string, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" {
		return defaultDeploymentErasePolicy, nil
	}
	switch policy {
	case "none", "quick", "full", "external_verified":
		return policy, nil
	default:
		return "", fmt.Errorf("erase_policy must be one of none, quick, full, external_verified")
	}
}

func deploymentServerStatusDeployable(status string) bool {
	return status == "ready" || status == "running" || status == "maintenance"
}

func activeDeploymentStatuses() []string {
	return []string{"pending", "running"}
}

func (h Handler) listDeployments(c *gin.Context) {
	query := h.db.Model(&models.Deployment{})
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("server_id"); value != "" {
		query = query.Where("server_id = ?", value)
	}
	if value := c.Query("image_id"); value != "" {
		query = query.Where("image_id = ?", value)
	}
	if value := c.Query("network_id"); value != "" {
		query = query.Where("network_id = ?", value)
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
		var rows []models.Deployment
		query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.Deployment
	query.Order("created_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createDeployment(c *gin.Context) {
	var req struct {
		ServerID uint `json:"server_id" binding:"required"`
		deploymentCreatePayload
	}
	if !bind(c, &req) {
		return
	}
	variables, erasePolicy, ok := h.validateDeploymentCreatePayload(c, req.ServerID, req.deploymentCreatePayload)
	if !ok {
		return
	}
	if !h.requireDeploymentSlot(c, req.ServerID) {
		return
	}
	if problems := h.deploymentPreflight(c, req.ServerID, req.ImageID, req.TemplateID, req.WorkflowID, req.NetworkID); len(problems) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment preflight failed", "problems": problems})
		return
	}
	id, actor := middleware.Actor(c)
	eraseConfirmedAt := time.Now().UTC()
	params := deploymentCreateParams{ServerID: req.ServerID, ImageID: req.ImageID, TemplateID: req.TemplateID, WorkflowID: req.WorkflowID, NetworkID: req.NetworkID, Variables: variables, ErasePolicy: erasePolicy, EraseConfirmedAt: eraseConfirmedAt, RequestedBy: actor}
	var row models.Deployment
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		created, err := h.createDeploymentInTx(tx, params)
		row = created
		return err
	}); err != nil {
		h.writeDeploymentCreateError(c, err)
		return
	}
	h.audit.Record(id, actor, "deployment.create", "deployment", row.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	h.workflow.StartDeployment(row.ID)
	c.JSON(http.StatusCreated, row)
}

func (h Handler) createDeploymentBatch(c *gin.Context) {
	var req struct {
		ServerIDs []uint `json:"server_ids" binding:"required"`
		deploymentCreatePayload
	}
	if !bind(c, &req) {
		return
	}
	serverIDs, err := normalizeDeploymentServerIDs(req.ServerIDs, maxBatchDeployments)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	variables, erasePolicy, ok := h.validateDeploymentCreatePayload(c, 1, req.deploymentCreatePayload)
	if !ok {
		return
	}
	var problems []string
	for _, serverID := range serverIDs {
		for _, problem := range h.deploymentPreflight(c, serverID, req.ImageID, req.TemplateID, req.WorkflowID, req.NetworkID) {
			problems = append(problems, fmt.Sprintf("server %d: %s", serverID, problem))
		}
	}
	if len(problems) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment preflight failed", "problems": problems})
		return
	}
	id, actor := middleware.Actor(c)
	eraseConfirmedAt := time.Now().UTC()
	rows := make([]models.Deployment, 0, len(serverIDs))
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		for _, serverID := range serverIDs {
			row, err := h.createDeploymentInTx(tx, deploymentCreateParams{ServerID: serverID, ImageID: req.ImageID, TemplateID: req.TemplateID, WorkflowID: req.WorkflowID, NetworkID: req.NetworkID, Variables: variables, ErasePolicy: erasePolicy, EraseConfirmedAt: eraseConfirmedAt, RequestedBy: actor})
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return nil
	}); err != nil {
		h.writeDeploymentCreateError(c, err)
		return
	}
	for _, row := range rows {
		h.audit.Record(id, actor, "deployment.create", "deployment", row.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		h.workflow.StartDeployment(row.ID)
	}
	c.JSON(http.StatusCreated, gin.H{"created": len(rows), "deployments": rows})
}

func (h Handler) preflightDeployment(c *gin.Context) {
	var req deploymentPreflightPayload
	if !bind(c, &req) {
		return
	}
	serverIDs, ok := h.validateDeploymentPreflightPayload(c, req)
	if !ok {
		return
	}
	response := deploymentPreflightResponse{Status: "ok", Targets: make([]deploymentPreflightTarget, 0, len(serverIDs)), Problems: []string{}, Warnings: []string{}}
	for _, serverID := range serverIDs {
		target := h.buildDeploymentPreflight(c, serverID, req.ImageID, req.TemplateID, req.WorkflowID, req.NetworkID)
		response.Targets = append(response.Targets, target)
		for _, problem := range target.Problems {
			response.Problems = append(response.Problems, fmt.Sprintf("server %d: %s", serverID, problem))
		}
		for _, warning := range target.Warnings {
			response.Warnings = append(response.Warnings, fmt.Sprintf("server %d: %s", serverID, warning))
		}
		if len(target.Problems) > 0 {
			response.Status = "blocked"
		}
	}
	c.JSON(http.StatusOK, response)
}

func (h Handler) validateDeploymentPreflightPayload(c *gin.Context, req deploymentPreflightPayload) ([]uint, bool) {
	serverIDs := req.ServerIDs
	if len(serverIDs) == 0 && req.ServerID != 0 {
		serverIDs = []uint{req.ServerID}
	}
	if len(serverIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server_id or server_ids is required"})
		return nil, false
	}
	normalized, err := normalizeDeploymentServerIDs(serverIDs, maxBatchDeployments)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	if req.ImageID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "image_id is required"})
		return nil, false
	}
	if req.TemplateID != nil && *req.TemplateID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template_id must be greater than 0"})
		return nil, false
	}
	if req.WorkflowID != nil && *req.WorkflowID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id must be greater than 0"})
		return nil, false
	}
	if req.NetworkID != nil && *req.NetworkID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "network_id must be greater than 0"})
		return nil, false
	}
	if _, err := normalizeOptionalJSONObject(req.Variables, "variables"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	if _, err := normalizeDeploymentErasePolicy(req.ErasePolicy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return normalized, true
}

func (h Handler) validateDeploymentCreatePayload(c *gin.Context, serverID uint, req deploymentCreatePayload) (datatypes.JSON, string, bool) {
	if serverID == 0 || req.ImageID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server_id and image_id are required"})
		return nil, "", false
	}
	if req.TemplateID != nil && *req.TemplateID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template_id must be greater than 0"})
		return nil, "", false
	}
	if req.WorkflowID != nil && *req.WorkflowID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workflow_id must be greater than 0"})
		return nil, "", false
	}
	if req.NetworkID != nil && *req.NetworkID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "network_id must be greater than 0"})
		return nil, "", false
	}
	variables, err := normalizeOptionalJSONObject(req.Variables, "variables")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, "", false
	}
	erasePolicy, err := normalizeDeploymentErasePolicy(req.ErasePolicy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, "", false
	}
	if !req.EraseConfirmed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "erase_confirmed must be true before creating deployment"})
		return nil, "", false
	}
	return variables, erasePolicy, true
}

func (h Handler) createDeploymentInTx(tx *gorm.DB, params deploymentCreateParams) (models.Deployment, error) {
	row := models.Deployment{ServerID: params.ServerID, ImageID: params.ImageID, TemplateID: params.TemplateID, WorkflowID: params.WorkflowID, NetworkID: params.NetworkID, Variables: params.Variables, ErasePolicy: params.ErasePolicy, EraseConfirmed: true, EraseConfirmedAt: &params.EraseConfirmedAt, Status: "pending", RequestedBy: params.RequestedBy}
	var server models.Server
	serverQuery := tx
	if tx.Dialector.Name() == "postgres" {
		serverQuery = serverQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := serverQuery.First(&server, params.ServerID).Error; err != nil {
		return row, err
	}
	if !deploymentServerStatusDeployable(server.Status) {
		return row, deploymentConflictError(fmt.Sprintf("server %d status is not deployable: %s", params.ServerID, server.Status))
	}
	var activeDeployments int64
	if err := tx.Model(&models.Deployment{}).Where("server_id = ? AND status IN ?", params.ServerID, activeDeploymentStatuses()).Count(&activeDeployments).Error; err != nil {
		return row, err
	}
	if activeDeployments > 0 {
		return row, deploymentConflictError(fmt.Sprintf("server %d already has an active deployment", params.ServerID))
	}
	if err := tx.Create(&row).Error; err != nil {
		return row, err
	}
	if _, err := h.boot.WithDB(tx).EnsureMetadataToken(row.ServerID, &row.ID); err != nil {
		return row, fmt.Errorf("metadata token initialization failed: %w", err)
	}
	beforeStatus := server.Status
	if err := tx.Model(&server).Update("status", "deploying").Error; err != nil {
		return row, err
	}
	return row, h.statusHistory.WithDB(tx).Record(server.ID, beforeStatus, "deploying", "deployment.create", params.RequestedBy)
}

func (h Handler) writeDeploymentCreateError(c *gin.Context, err error) {
	var conflict deploymentConflictError
	if errors.As(err, &conflict) {
		c.JSON(http.StatusConflict, gin.H{"error": conflict.Error()})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func normalizeDeploymentServerIDs(ids []uint, limit int) ([]uint, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("server_ids cannot be empty")
	}
	if len(ids) > limit {
		return nil, fmt.Errorf("cannot create more than %d deployments in one batch", limit)
	}
	seen := map[uint]bool{}
	normalized := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, fmt.Errorf("server_ids cannot contain 0")
		}
		if seen[id] {
			return nil, fmt.Errorf("server_ids contains duplicate server %d", id)
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func (h Handler) deploymentPreflight(c *gin.Context, serverID, imageID uint, templateID *uint, workflowID *uint, networkID *uint) []string {
	return h.buildDeploymentPreflight(c, serverID, imageID, templateID, workflowID, networkID).Problems
}

func (h Handler) buildDeploymentPreflight(c *gin.Context, serverID, imageID uint, templateID *uint, workflowID *uint, networkID *uint) deploymentPreflightTarget {
	result := deploymentPreflightTarget{ServerID: serverID, Status: "ok", PowerControl: "manual", Checks: []deploymentPreflightCheck{}, Problems: []string{}, Warnings: []string{}, OperatorActions: []string{}}
	problems := h.images.Preflight(serverID, imageID, templateID, workflowID)
	result.Problems = append(result.Problems, problems...)
	if preflightHas(problems, "server not found") {
		result.Status = "blocked"
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "target", Status: "error", Message: "server not found"})
		return result
	}
	if len(problems) > 0 {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "target", Status: "error", Message: strings.Join(problems, "; ")})
	} else {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "target", Status: "ok", Message: "server, image, templates, and default deployment network are deployable"})
	}
	activeProblems := h.deploymentActiveDeploymentPreflight(serverID)
	result.Problems = append(result.Problems, activeProblems...)
	if len(activeProblems) > 0 {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "active_deployment", Status: "error", Message: strings.Join(activeProblems, "; ")})
	} else {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "active_deployment", Status: "ok", Message: "no active deployment is using this server"})
	}
	networkProblems := h.deploymentNetworkPreflight(networkID)
	result.Problems = append(result.Problems, networkProblems...)
	if len(networkProblems) > 0 {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "network", Status: "error", Message: strings.Join(networkProblems, "; ")})
	} else if networkID != nil {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "network", Status: "ok", Message: "selected deployment network is enabled"})
	}
	var bmcEndpoint models.BmcEndpoint
	bmcLookup := h.db.Where("server_id = ?", serverID).Limit(1).Find(&bmcEndpoint)
	if bmcLookup.Error != nil {
		problem := "bmc endpoint lookup failed: " + bmcLookup.Error.Error()
		result.Problems = append(result.Problems, problem)
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "bmc", Status: "error", Message: problem})
		result.Status = deploymentPreflightStatus(result.Problems)
		return result
	}
	if bmcLookup.RowsAffected == 0 {
		warning := "bmc endpoint not configured; use manual power control, PXE boot, and SSH or physical evidence for this target"
		result.Warnings = append(result.Warnings, warning)
		result.OperatorActions = append(result.OperatorActions, manualDeploymentOperatorActions()...)
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "bmc", Status: "warning", Message: warning})
		result.Status = deploymentPreflightStatus(result.Problems)
		return result
	}
	result.PowerControl = "bmc"
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.bmc.Check(ctx, fmt.Sprint(serverID)); err != nil {
		problem := "bmc connectivity check failed: " + err.Error()
		result.Problems = append(result.Problems, problem)
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "bmc", Status: "error", Message: problem})
	} else {
		result.Checks = append(result.Checks, deploymentPreflightCheck{Name: "bmc", Status: "ok", Message: fmt.Sprintf("%s endpoint is reachable", bmcEndpoint.Type)})
	}
	result.Status = deploymentPreflightStatus(result.Problems)
	return result
}

func manualDeploymentOperatorActions() []string {
	return []string{
		"人工开机或重启目标服务器，并在启动菜单选择部署网卡 PXE/Network Boot",
		"确认平台收到该服务器 MAC 对应的 PXE/iPXE 启动事件",
		"安装完成后通过 SSH 检查或录入物理证据完成验收闭环",
	}
}

func deploymentPreflightStatus(problems []string) string {
	if len(problems) > 0 {
		return "blocked"
	}
	return "ok"
}

func (h Handler) deploymentActiveDeploymentPreflight(serverID uint) []string {
	var activeDeployments int64
	if err := h.db.Model(&models.Deployment{}).Where("server_id = ? AND status IN ?", serverID, activeDeploymentStatuses()).Count(&activeDeployments).Error; err != nil {
		return []string{"active deployment lookup failed: " + err.Error()}
	}
	if activeDeployments > 0 {
		return []string{"server already has an active deployment"}
	}
	return nil
}

func (h Handler) deploymentNetworkPreflight(networkID *uint) []string {
	if networkID == nil {
		return nil
	}
	var network models.NetworkConfig
	if err := h.db.First(&network, *networkID).Error; err != nil {
		return []string{"deployment network not found"}
	}
	if network.Purpose != "deployment" || network.Status != "enabled" {
		return []string{"deployment network must be an enabled deployment network"}
	}
	copy := network
	if err := validateAndNormalizeNetworkConfig(&copy); err != nil {
		return []string{"deployment network is invalid: " + err.Error()}
	}
	if err := ensureNetworkCIDRDoesNotOverlap(h.db, &copy); err != nil {
		return []string{"deployment network is invalid: " + err.Error()}
	}
	return nil
}

func preflightHas(problems []string, problem string) bool {
	for _, item := range problems {
		if item == problem {
			return true
		}
	}
	return false
}

func (h Handler) requireDeploymentSlot(c *gin.Context, serverID uint) bool {
	var server models.Server
	if err := h.db.Select("id", "status").First(&server, serverID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	if !deploymentServerStatusDeployable(server.Status) {
		c.JSON(http.StatusConflict, gin.H{"error": "server status is not deployable: " + server.Status})
		return false
	}
	var activeDeployments int64
	if err := h.db.Model(&models.Deployment{}).Where("server_id = ? AND status IN ?", serverID, activeDeploymentStatuses()).Count(&activeDeployments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	if activeDeployments > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "server already has an active deployment"})
		return false
	}
	return true
}

func (h Handler) getDeployment(c *gin.Context) {
	var row models.Deployment
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	_ = h.workflow.ReconcileDeployment(row.ID)
	if err := h.db.First(&row, row.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h Handler) cancelDeployment(c *gin.Context) {
	now := time.Now().UTC()
	var deployment models.Deployment
	if notFound(c, h.db.First(&deployment, c.Param("id")).Error) {
		return
	}
	result := h.db.Model(&deployment).Where("status IN ?", []string{"pending", "running"}).Updates(map[string]any{"status": "cancelled", "finished_at": now})
	if result.RowsAffected == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment cannot be cancelled"})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "deployment.cancel", "deployment", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	var server models.Server
	if err := h.db.First(&server, deployment.ServerID).Error; err == nil && server.Status == "deploying" {
		h.db.Model(&server).Update("status", "ready")
		_ = h.statusHistory.Record(server.ID, "deploying", "ready", "deployment.cancel", email)
	}
	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

func (h Handler) retryDeployment(c *gin.Context) {
	var deployment models.Deployment
	if notFound(c, h.db.First(&deployment, c.Param("id")).Error) {
		return
	}
	if deployment.Status != "failed" && deployment.Status != "cancelled" {
		c.JSON(http.StatusConflict, gin.H{"error": "deployment is not retryable"})
		return
	}
	if !h.requireDeploymentSlot(c, deployment.ServerID) {
		return
	}
	if problems := h.deploymentPreflight(c, deployment.ServerID, deployment.ImageID, deployment.TemplateID, deployment.WorkflowID, deployment.NetworkID); len(problems) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment preflight failed", "problems": problems})
		return
	}
	id, actor := middleware.Actor(c)
	now := time.Now().UTC()
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		var server models.Server
		serverQuery := tx
		if tx.Dialector.Name() == "postgres" {
			serverQuery = serverQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := serverQuery.First(&server, deployment.ServerID).Error; err != nil {
			return err
		}
		if !deploymentServerStatusDeployable(server.Status) {
			return deploymentConflictError("server status is not deployable: " + server.Status)
		}
		var activeDeployments int64
		if err := tx.Model(&models.Deployment{}).Where("server_id = ? AND status IN ? AND id <> ?", deployment.ServerID, activeDeploymentStatuses(), deployment.ID).Count(&activeDeployments).Error; err != nil {
			return err
		}
		if activeDeployments > 0 {
			return deploymentConflictError("server already has an active deployment")
		}
		if err := tx.Model(&deployment).Updates(map[string]any{"status": "pending", "started_at": nil, "finished_at": nil, "error_message": ""}).Error; err != nil {
			return err
		}
		if _, err := h.boot.WithDB(tx).EnsureMetadataToken(deployment.ServerID, &deployment.ID); err != nil {
			return fmt.Errorf("metadata token initialization failed: %w", err)
		}
		beforeStatus := server.Status
		if err := tx.Model(&server).Update("status", "deploying").Error; err != nil {
			return err
		}
		return h.statusHistory.WithDB(tx).Record(server.ID, beforeStatus, "deploying", "deployment.retry", actor)
	}); err != nil {
		var conflict deploymentConflictError
		if errors.As(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{"error": conflict.Error()})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.First(&deployment, deployment.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(id, actor, "deployment.retry", "deployment", deployment.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	h.workflow.StartDeployment(deployment.ID)
	deployment.StartedAt = nil
	deployment.FinishedAt = nil
	deployment.UpdatedAt = now
	c.JSON(http.StatusOK, deployment)
}

func (h Handler) deploymentLogs(c *gin.Context) {
	var deployment models.Deployment
	if notFound(c, h.db.First(&deployment, c.Param("id")).Error) {
		return
	}
	_ = h.workflow.ReconcileDeployment(deployment.ID)
	if err := h.db.First(&deployment, deployment.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var runs []models.WorkflowRun
	h.db.Where("deployment_id = ?", deployment.ID).Order("id asc").Find(&runs)
	if len(runs) == 0 {
		c.JSON(http.StatusOK, gin.H{"summary": deploymentLogSummary{DeploymentID: deployment.ID, Status: deployment.Status}, "workflow": nil, "runs": []workflowRunLog{}, "tasks": []taskExecutionLog{}})
		return
	}

	runIDs := make([]uint, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	var allTasks []models.TaskExecution
	h.db.Where("workflow_run_id IN ?", runIDs).Order("workflow_run_id asc, id asc").Find(&allTasks)
	tasksByRun := map[uint][]models.TaskExecution{}
	for _, task := range allTasks {
		tasksByRun[task.WorkflowRunID] = append(tasksByRun[task.WorkflowRunID], task)
	}

	now := time.Now().UTC()
	runLogs := make([]workflowRunLog, 0, len(runs))
	for i, run := range runs {
		runLogs = append(runLogs, workflowRunLogFromModel(run, i+1, tasksByRun[run.ID], now))
	}
	latestRun := runs[len(runs)-1]
	latestTasks := taskExecutionLogs(tasksByRun[latestRun.ID], now)
	summary := deploymentLogSummaryFrom(deployment, runLogs[len(runLogs)-1], len(runLogs))
	c.JSON(http.StatusOK, gin.H{"summary": summary, "workflow": runLogs[len(runLogs)-1], "runs": runLogs, "tasks": latestTasks})
}

type deploymentLogSummary struct {
	DeploymentID  uint       `json:"deployment_id"`
	Status        string     `json:"status"`
	LatestRunID   uint       `json:"latest_run_id,omitempty"`
	TotalRuns     int        `json:"total_runs"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	DurationMS    *int64     `json:"duration_ms,omitempty"`
	TaskTotal     int        `json:"task_total"`
	TaskSuccess   int        `json:"task_success"`
	TaskFailed    int        `json:"task_failed"`
	TaskCancelled int        `json:"task_cancelled"`
	TaskRunning   int        `json:"task_running"`
	TaskPending   int        `json:"task_pending"`
}

type workflowRunLog struct {
	ID            uint       `json:"id"`
	Attempt       int        `json:"attempt"`
	DeploymentID  uint       `json:"deployment_id"`
	Name          string     `json:"name"`
	Version       string     `json:"version"`
	Status        string     `json:"status"`
	Definition    string     `json:"definition"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	DurationMS    *int64     `json:"duration_ms,omitempty"`
	TaskTotal     int        `json:"task_total"`
	TaskSuccess   int        `json:"task_success"`
	TaskFailed    int        `json:"task_failed"`
	TaskCancelled int        `json:"task_cancelled"`
	TaskRunning   int        `json:"task_running"`
	TaskPending   int        `json:"task_pending"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type taskExecutionLog struct {
	ID            uint       `json:"id"`
	WorkflowRunID uint       `json:"workflow_run_id"`
	StepName      string     `json:"step_name"`
	Action        string     `json:"action"`
	Status        string     `json:"status"`
	RetryCount    int        `json:"retry_count"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	DurationMS    *int64     `json:"duration_ms,omitempty"`
	Stdout        string     `json:"stdout"`
	Stderr        string     `json:"stderr"`
	ErrorMessage  string     `json:"error_message"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func workflowRunLogFromModel(run models.WorkflowRun, attempt int, tasks []models.TaskExecution, now time.Time) workflowRunLog {
	log := workflowRunLog{
		ID:           run.ID,
		Attempt:      attempt,
		DeploymentID: run.DeploymentID,
		Name:         run.Name,
		Version:      run.Version,
		Status:       run.Status,
		Definition:   run.Definition,
		StartedAt:    run.StartedAt,
		FinishedAt:   run.FinishedAt,
		DurationMS:   durationMilliseconds(run.StartedAt, run.FinishedAt, now),
		CreatedAt:    run.CreatedAt,
		UpdatedAt:    run.UpdatedAt,
	}
	for _, task := range tasks {
		log.TaskTotal++
		switch task.Status {
		case "success":
			log.TaskSuccess++
		case "failed":
			log.TaskFailed++
		case "cancelled":
			log.TaskCancelled++
		case "running":
			log.TaskRunning++
		case "pending":
			log.TaskPending++
		}
	}
	return log
}

func taskExecutionLogs(tasks []models.TaskExecution, now time.Time) []taskExecutionLog {
	logs := make([]taskExecutionLog, 0, len(tasks))
	for _, task := range tasks {
		logs = append(logs, taskExecutionLog{
			ID:            task.ID,
			WorkflowRunID: task.WorkflowRunID,
			StepName:      task.StepName,
			Action:        task.Action,
			Status:        task.Status,
			RetryCount:    task.RetryCount,
			StartedAt:     task.StartedAt,
			FinishedAt:    task.FinishedAt,
			DurationMS:    durationMilliseconds(task.StartedAt, task.FinishedAt, now),
			Stdout:        task.Stdout,
			Stderr:        task.Stderr,
			ErrorMessage:  task.ErrorMessage,
			CreatedAt:     task.CreatedAt,
			UpdatedAt:     task.UpdatedAt,
		})
	}
	return logs
}

func deploymentLogSummaryFrom(deployment models.Deployment, latest workflowRunLog, totalRuns int) deploymentLogSummary {
	return deploymentLogSummary{
		DeploymentID:  deployment.ID,
		Status:        deployment.Status,
		LatestRunID:   latest.ID,
		TotalRuns:     totalRuns,
		StartedAt:     deployment.StartedAt,
		FinishedAt:    deployment.FinishedAt,
		DurationMS:    latest.DurationMS,
		TaskTotal:     latest.TaskTotal,
		TaskSuccess:   latest.TaskSuccess,
		TaskFailed:    latest.TaskFailed,
		TaskCancelled: latest.TaskCancelled,
		TaskRunning:   latest.TaskRunning,
		TaskPending:   latest.TaskPending,
	}
}

func durationMilliseconds(startedAt *time.Time, finishedAt *time.Time, now time.Time) *int64 {
	if startedAt == nil {
		return nil
	}
	finished := now
	if finishedAt != nil {
		finished = *finishedAt
	}
	if finished.Before(*startedAt) {
		return nil
	}
	ms := finished.Sub(*startedAt).Milliseconds()
	return &ms
}
