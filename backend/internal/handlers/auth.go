package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func (h Handler) login(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if !bind(c, &req) {
		return
	}
	loginEmail := strings.ToLower(strings.TrimSpace(req.Email))
	rateKey := loginEmail + "|" + c.ClientIP()
	if ok, retryAfter := h.loginLimiter.allow(rateKey, time.Now().UTC()); !ok {
		retrySeconds := int(retryAfter.Seconds())
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		h.audit.Record(0, loginEmail, "auth.login.blocked", "user", loginEmail, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		c.Header("Retry-After", strconv.Itoa(retrySeconds))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed login attempts", "retry_after_seconds": retrySeconds})
		return
	}
	token, user, err := h.auth.Login(req.Email, req.Password)
	if err != nil {
		h.loginLimiter.recordFailure(rateKey, time.Now().UTC())
		h.audit.Record(0, loginEmail, "auth.login.failed", "user", loginEmail, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	h.loginLimiter.reset(rateKey)
	h.audit.Record(user.ID, user.Email, "auth.login", "user", user.ID, "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

func (h Handler) me(c *gin.Context) {
	id, email := middleware.Actor(c)
	c.JSON(http.StatusOK, gin.H{"id": id, "email": email, "role": c.GetString("user_role")})
}

func (h Handler) dashboard(c *gin.Context) {
	var servers, images, deployments, alerts, audits int64
	h.db.Model(&models.Server{}).Count(&servers)
	h.db.Model(&models.Image{}).Count(&images)
	h.db.Model(&models.Deployment{}).Count(&deployments)
	h.db.Model(&models.Alert{}).Where("status <> ?", "resolved").Count(&alerts)
	h.db.Model(&models.AuditLog{}).Count(&audits)
	var recentDeployments []models.Deployment
	var recentAuditLogs []models.AuditLog
	h.db.Order("created_at desc").Limit(5).Find(&recentDeployments)
	h.db.Order("created_at desc").Limit(8).Find(&recentAuditLogs)
	c.JSON(http.StatusOK, gin.H{
		"servers":             servers,
		"images":              images,
		"deployments":         deployments,
		"active_alerts":       alerts,
		"audit_logs":          audits,
		"server_statuses":     h.countByModel(&models.Server{}, "status", ""),
		"deployment_statuses": h.countByModel(&models.Deployment{}, "status", ""),
		"alert_severities":    h.countByModel(&models.Alert{}, "severity", "status <> 'resolved'"),
		"recent_deployments":  recentDeployments,
		"recent_audit_logs":   recentAuditLogs,
	})
}

func (h Handler) countByModel(model any, column, where string) map[string]int64 {
	type bucket struct {
		Key   string
		Count int64
	}
	rows := []bucket{}
	query := h.db.Model(model).Select(column + " as key, count(*) as count").Group(column)
	if where != "" {
		query = query.Where(where)
	}
	query.Scan(&rows)
	result := map[string]int64{}
	for _, row := range rows {
		if row.Key == "" {
			row.Key = "unknown"
		}
		result[row.Key] = row.Count
	}
	return result
}
