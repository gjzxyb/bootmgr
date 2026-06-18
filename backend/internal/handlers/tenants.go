package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

func (h Handler) listTenants(c *gin.Context) {
	query := h.db.Model(&models.Tenant{})
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		query = query.Where("tenant_id LIKE ? OR name LIKE ? OR owner LIKE ?", "%"+value+"%", "%"+value+"%", "%"+value+"%")
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.Tenant
		query.Order("updated_at desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.Tenant
	query.Order("updated_at desc, id desc").Limit(200).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createTenant(c *gin.Context) {
	var req struct {
		TenantID    string         `json:"tenant_id" binding:"required"`
		Name        string         `json:"name" binding:"required"`
		Status      string         `json:"status"`
		Owner       string         `json:"owner"`
		Description string         `json:"description"`
		Quota       datatypes.JSON `json:"quota"`
	}
	if !bind(c, &req) {
		return
	}
	row := models.Tenant{TenantID: req.TenantID, Name: req.Name, Status: req.Status, Owner: req.Owner, Description: req.Description, Quota: req.Quota}
	if err := normalizeTenant(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "tenant.create", "tenant", row.TenantID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) updateTenant(c *gin.Context) {
	var row models.Tenant
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]json.RawMessage
	if !bind(c, &req) {
		return
	}
	if err := applyTenantPatch(&row, req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeTenant(&row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateTenantQuotaAllowsCurrentUsage(h.db, row); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "tenant.update", "tenant", row.TenantID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func normalizeTenant(row *models.Tenant) error {
	row.TenantID = strings.TrimSpace(row.TenantID)
	row.Name = strings.TrimSpace(row.Name)
	row.Status = strings.TrimSpace(row.Status)
	row.Owner = strings.TrimSpace(row.Owner)
	row.Description = strings.TrimSpace(row.Description)
	if row.Status == "" {
		row.Status = "active"
	}
	if row.TenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if !tenantIDPattern.MatchString(row.TenantID) {
		return fmt.Errorf("tenant_id may contain only letters, numbers, dots, underscores, and dashes")
	}
	if row.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !stringIn(row.Status, "active", "disabled") {
		return fmt.Errorf("status must be active or disabled")
	}
	quota, err := normalizeTenantQuota(row.Quota)
	if err != nil {
		return err
	}
	row.Quota = quota
	return nil
}

func applyTenantPatch(row *models.Tenant, req map[string]json.RawMessage) error {
	for key, raw := range req {
		switch key {
		case "id", "tenant_id", "created_at", "updated_at":
			continue
		case "name":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Name = value
		case "status":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Status = value
		case "owner":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Owner = value
		case "description":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return err
			}
			row.Description = value
		case "quota":
			row.Quota = jsonFromRaw(raw)
		default:
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	return nil
}

func normalizeTenantQuota(raw datatypes.JSON) (datatypes.JSON, error) {
	normalized, err := normalizeOptionalJSONObject(raw, "quota")
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	var quota map[string]any
	if err := json.Unmarshal(normalized, &quota); err != nil {
		return nil, fmt.Errorf("quota must be a JSON object: %w", err)
	}
	if err := validateTenantQuotaValue("quota", quota); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateTenantQuotaValue(path string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, nested := range v {
			if err := validateTenantQuotaValue(path+"."+key, nested); err != nil {
				return err
			}
		}
	case float64:
		if v < 0 {
			return fmt.Errorf("%s must be non-negative", path)
		}
		if path == "quota.servers" && v != math.Trunc(v) {
			return fmt.Errorf("%s must be an integer", path)
		}
	default:
		if path == "quota.servers" {
			return fmt.Errorf("%s must be a number", path)
		}
	}
	return nil
}

func validateTenantQuotaAllowsCurrentUsage(db *gorm.DB, tenant models.Tenant) error {
	limit, ok, err := tenantServerQuotaLimit(tenant)
	if err != nil || !ok {
		return err
	}
	count, err := countTenantServers(db, tenant.TenantID, 0)
	if err != nil {
		return err
	}
	if count > limit {
		return fmt.Errorf("tenant %q server quota cannot be lower than current usage: limit %d, current %d", tenant.TenantID, limit, count)
	}
	return nil
}

func enforceTenantServerQuota(db *gorm.DB, tenantID string, excludeServerID uint) error {
	tenant, err := activeTenant(db, tenantID)
	if err != nil || tenant.TenantID == "" {
		return err
	}
	limit, ok, err := tenantServerQuotaLimit(tenant)
	if err != nil || !ok {
		return err
	}
	count, err := countTenantServers(db, tenant.TenantID, excludeServerID)
	if err != nil {
		return err
	}
	if count+1 > limit {
		return fmt.Errorf("tenant %q server quota exceeded: limit %d, current %d", tenant.TenantID, limit, count)
	}
	return nil
}

func activeTenant(db *gorm.DB, tenantID string) (models.Tenant, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return models.Tenant{}, nil
	}
	var tenant models.Tenant
	if err := db.Where("tenant_id = ?", tenantID).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tenant, fmt.Errorf("tenant %q does not exist", tenantID)
		}
		return tenant, err
	}
	if tenant.Status != "active" {
		return tenant, fmt.Errorf("tenant %q is not active", tenantID)
	}
	return tenant, nil
}

func tenantServerQuotaLimit(tenant models.Tenant) (int64, bool, error) {
	if len(tenant.Quota) == 0 {
		return 0, false, nil
	}
	var quota map[string]any
	if err := json.Unmarshal(tenant.Quota, &quota); err != nil {
		return 0, false, fmt.Errorf("tenant %q quota is invalid: %w", tenant.TenantID, err)
	}
	raw, ok := quota["servers"]
	if !ok || raw == nil {
		return 0, false, nil
	}
	value, ok := raw.(float64)
	if !ok {
		return 0, false, fmt.Errorf("tenant %q quota.servers must be a number", tenant.TenantID)
	}
	if value < 0 || value != math.Trunc(value) {
		return 0, false, fmt.Errorf("tenant %q quota.servers must be a non-negative integer", tenant.TenantID)
	}
	return int64(value), true, nil
}

func countTenantServers(db *gorm.DB, tenantID string, excludeServerID uint) (int64, error) {
	var count int64
	query := db.Model(&models.Server{}).Where("tenant_id = ?", tenantID)
	if excludeServerID != 0 {
		query = query.Where("id <> ?", excludeServerID)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
