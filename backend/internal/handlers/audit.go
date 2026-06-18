package handlers

import (
	"net/http"
	"strconv"

	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func (h Handler) listAuditLogs(c *gin.Context) {
	query := h.db.Model(&models.AuditLog{})
	if value := c.Query("action"); value != "" {
		query = query.Where("action = ?", value)
	}
	if value := c.Query("actor_email"); value != "" {
		query = query.Where("actor_email = ?", value)
	}
	if value := c.Query("resource_type"); value != "" {
		query = query.Where("resource_type = ?", value)
	}
	if value := c.Query("risk_level"); value != "" {
		query = query.Where("risk_level = ?", value)
	}
	if c.Query("page") == "" && c.Query("page_size") == "" {
		var rows []models.AuditLog
		query.Order("created_at desc").Limit(300).Find(&rows)
		c.JSON(http.StatusOK, rows)
		return
	}
	page := positiveInt(c.Query("page"), 1)
	pageSize := positiveInt(c.Query("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	var total int64
	query.Count(&total)
	var rows []models.AuditLog
	query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (h Handler) getAuditLog(c *gin.Context) {
	var row models.AuditLog
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	c.JSON(http.StatusOK, row)
}

func positiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
