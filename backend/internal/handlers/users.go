package handlers

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"unicode"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func (h Handler) listUsers(c *gin.Context) {
	query := h.db.Model(&models.User{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		query = query.Where("email LIKE ? OR name LIKE ?", "%"+value+"%", "%"+value+"%")
	}
	if value := c.Query("role"); value != "" {
		query = query.Where("role = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.User
		query.Order("created_at desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.User
	query.Order("created_at desc, id desc").Limit(200).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createUser(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required"`
		Name     string `json:"name" binding:"required"`
		Role     string `json:"role" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if !bind(c, &req) {
		return
	}
	req.Email = normalizeEmail(req.Email)
	req.Name = strings.TrimSpace(req.Name)
	req.Role = strings.TrimSpace(req.Role)
	if addr, err := mail.ParseAddress(req.Email); err != nil || addr.Address != req.Email {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if !validRole(req.Role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role"})
		return
	}
	if err := validateUserPassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row := models.User{Email: req.Email, Name: req.Name, Role: req.Role, PasswordHash: string(hash)}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "user.create", "user", row.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) updateUser(c *gin.Context) {
	var row models.User
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if !bind(c, &req) {
		return
	}
	updates := map[string]any{}
	req.Name = strings.TrimSpace(req.Name)
	req.Role = strings.TrimSpace(req.Role)
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Role != "" {
		if !validRole(req.Role) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role"})
			return
		}
		if row.Role == "admin" && req.Role != "admin" {
			var remainingAdmins int64
			if err := h.db.Model(&models.User{}).Where("role = ? AND id <> ?", "admin", row.ID).Count(&remainingAdmins).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if remainingAdmins == 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "cannot demote the last admin user"})
				return
			}
		}
		updates["role"] = req.Role
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, row)
		return
	}
	if err := h.db.Model(&row).Updates(updates).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.db.First(&row, row.ID)
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "user.update", "user", row.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func (h Handler) resetUserPassword(c *gin.Context) {
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if !bind(c, &req) {
		return
	}
	if err := validateUserPassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result := h.db.Model(&models.User{}).Where("id = ?", c.Param("id")).Update("password_hash", string(hash))
	if result.Error != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "user.password.reset", "user", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func validRole(role string) bool {
	return role == "admin" || role == "operator" || role == "viewer"
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateUserPassword(password string) error {
	if len([]rune(password)) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		if unicode.IsUpper(r) {
			hasUpper = true
		}
		if unicode.IsLower(r) {
			hasLower = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return errors.New("password must include uppercase, lowercase, and digit characters")
	}
	return nil
}
