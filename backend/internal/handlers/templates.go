package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

func (h Handler) listInstallTemplates(c *gin.Context) {
	query := h.db.Model(&models.InstallTemplate{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR os_version LIKE ? OR content LIKE ?", like, like, like)
	}
	if value := c.Query("os_family"); value != "" {
		query = query.Where("os_family = ?", value)
	}
	if value := c.Query("template_type"); value != "" {
		query = query.Where("template_type = ?", value)
	}
	if value := c.Query("version"); value != "" {
		query = query.Where("version = ?", value)
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
		var rows []models.InstallTemplate
		query.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.InstallTemplate
	query.Order("updated_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}
func (h Handler) createInstallTemplate(c *gin.Context) {
	var row models.InstallTemplate
	if !bind(c, &row) {
		return
	}
	_, email := middleware.Actor(c)
	row.ID = 0
	row.CreatedBy = email
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	if err := normalizeInstallTemplate(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "install_template.create", "install_template", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}
func (h Handler) updateInstallTemplate(c *gin.Context) {
	var row models.InstallTemplate
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]json.RawMessage
	if !bind(c, &req) {
		return
	}
	if err := applyInstallTemplatePatch(&row, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeInstallTemplate(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "install_template.update", "install_template", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func (h Handler) deleteInstallTemplate(c *gin.Context) {
	var row models.InstallTemplate
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var deployments int64
	if err := h.db.Model(&models.Deployment{}).Where("template_id = ?", row.ID).Count(&deployments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if deployments > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("install template is referenced by %d deployment(s); disable it instead of deleting", deployments)})
		return
	}
	if err := h.db.Delete(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "install_template.delete", "install_template", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.Status(http.StatusNoContent)
}

func (h Handler) listWorkflowTemplates(c *gin.Context) {
	query := h.db.Model(&models.WorkflowTemplate{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	if value := c.Query("version"); value != "" {
		query = query.Where("version = ?", value)
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
		var rows []models.WorkflowTemplate
		query.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.WorkflowTemplate
	query.Order("updated_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}
func (h Handler) createWorkflowTemplate(c *gin.Context) {
	var row models.WorkflowTemplate
	if !bind(c, &row) {
		return
	}
	_, email := middleware.Actor(c)
	row.ID = 0
	row.CreatedBy = email
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	if err := normalizeWorkflowTemplate(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "workflow_template.create", "workflow_template", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}
func (h Handler) updateWorkflowTemplate(c *gin.Context) {
	var row models.WorkflowTemplate
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]json.RawMessage
	if !bind(c, &req) {
		return
	}
	if err := applyWorkflowTemplatePatch(&row, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeWorkflowTemplate(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "workflow_template.update", "workflow_template", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func (h Handler) deleteWorkflowTemplate(c *gin.Context) {
	var row models.WorkflowTemplate
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var deployments int64
	if err := h.db.Model(&models.Deployment{}).Where("workflow_id = ?", row.ID).Count(&deployments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if deployments > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("workflow template is referenced by %d deployment(s); disable it instead of deleting", deployments)})
		return
	}
	if err := h.db.Delete(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "workflow_template.delete", "workflow_template", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.Status(http.StatusNoContent)
}

func normalizeInstallTemplate(row *models.InstallTemplate) error {
	row.Name = strings.TrimSpace(row.Name)
	row.OSFamily = strings.TrimSpace(row.OSFamily)
	row.OSVersion = strings.TrimSpace(row.OSVersion)
	row.TemplateType = strings.TrimSpace(row.TemplateType)
	row.Version = strings.TrimSpace(row.Version)
	row.Status = strings.TrimSpace(row.Status)
	if row.Version == "" {
		row.Version = "v1"
	}
	if row.Status == "" {
		row.Status = "enabled"
	}
	if row.Name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(row.Content) == "" {
		return fmt.Errorf("content is required")
	}
	if row.TemplateType == "" {
		return fmt.Errorf("template_type is required")
	}
	if !stringIn(row.TemplateType, "cloud-init", "autoinstall", "kickstart", "preseed", "unattend") {
		return fmt.Errorf("template_type must be one of cloud-init, autoinstall, kickstart, preseed, unattend")
	}
	if !stringIn(row.Status, "enabled", "disabled") {
		return fmt.Errorf("status must be enabled or disabled")
	}
	variablesSchema, err := normalizeOptionalJSONObject(row.VariablesSchema, "variables_schema")
	if err != nil {
		return err
	}
	row.VariablesSchema = variablesSchema
	return nil
}

func normalizeWorkflowTemplate(row *models.WorkflowTemplate) error {
	row.Name = strings.TrimSpace(row.Name)
	row.Version = strings.TrimSpace(row.Version)
	row.Description = strings.TrimSpace(row.Description)
	row.Status = strings.TrimSpace(row.Status)
	if row.Version == "" {
		row.Version = "v1"
	}
	if row.Status == "" {
		row.Status = "enabled"
	}
	if row.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !stringIn(row.Status, "enabled", "disabled") {
		return fmt.Errorf("status must be enabled or disabled")
	}
	definition, err := normalizeWorkflowDefinition(row.Definition)
	if err != nil {
		return err
	}
	row.Definition = definition
	return nil
}

func applyInstallTemplatePatch(row *models.InstallTemplate, req map[string]json.RawMessage) error {
	for key, raw := range req {
		switch key {
		case "id", "created_by", "created_at", "updated_at":
			continue
		case "name":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Name = value
		case "os_family":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.OSFamily = value
		case "os_version":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.OSVersion = value
		case "template_type":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.TemplateType = value
		case "content":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Content = value
		case "variables_schema":
			row.VariablesSchema = jsonFromRaw(raw)
		case "version":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Version = value
		case "status":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Status = value
		default:
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	return nil
}

func applyWorkflowTemplatePatch(row *models.WorkflowTemplate, req map[string]json.RawMessage) error {
	for key, raw := range req {
		switch key {
		case "id", "created_by", "created_at", "updated_at":
			continue
		case "name":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Name = value
		case "version":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Version = value
		case "description":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Description = value
		case "definition":
			row.Definition = jsonFromRaw(raw)
		case "status":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Status = value
		default:
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	return nil
}

func stringFromJSON(raw json.RawMessage, field string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return strings.TrimSpace(value), nil
}

func jsonFromRaw(raw json.RawMessage) datatypes.JSON {
	copied := append([]byte(nil), raw...)
	return datatypes.JSON(copied)
}

func normalizeOptionalJSONObject(raw datatypes.JSON, field string) (datatypes.JSON, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil || value == nil {
		if err != nil {
			return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
		}
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	return datatypes.JSON(normalized), nil
}

func normalizeWorkflowDefinition(raw datatypes.JSON) (datatypes.JSON, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("definition is required")
	}
	var definition map[string]any
	if err := json.Unmarshal([]byte(trimmed), &definition); err != nil || definition == nil {
		if err != nil {
			return nil, fmt.Errorf("definition must be a JSON object: %w", err)
		}
		return nil, fmt.Errorf("definition must be a JSON object")
	}
	rawSteps, ok := definition["steps"].([]any)
	if !ok || len(rawSteps) == 0 {
		return nil, fmt.Errorf("definition.steps must contain at least one step")
	}
	for i, rawStep := range rawSteps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("definition.steps[%d] must be an object", i)
		}
		name, ok := step["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("definition.steps[%d].name is required", i)
		}
		action, ok := step["action"].(string)
		if !ok || strings.TrimSpace(action) == "" {
			return nil, fmt.Errorf("definition.steps[%d].action is required", i)
		}
		step["name"] = strings.TrimSpace(name)
		step["action"] = strings.TrimSpace(action)
	}
	normalized, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("definition must be a JSON object: %w", err)
	}
	return datatypes.JSON(normalized), nil
}
