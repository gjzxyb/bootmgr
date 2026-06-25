package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func (h Handler) listImages(c *gin.Context) {
	query := h.db.Model(&models.Image{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR os_version LIKE ? OR sha256 LIKE ?", like, like, like)
	}
	if value := c.Query("os_family"); value != "" {
		query = query.Where("os_family = ?", value)
	}
	if value := c.Query("architecture"); value != "" {
		query = query.Where("architecture = ?", value)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("test_status"); value != "" {
		query = query.Where("test_status = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.Image
		query.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.Image
	query.Order("updated_at desc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createImage(c *gin.Context) {
	var row models.Image
	if !bind(c, &row) {
		return
	}
	id, email := middleware.Actor(c)
	row.ID = 0
	row.CreatedAt = time.Time{}
	row.UpdatedAt = time.Time{}
	row.DeletedAt = gorm.DeletedAt{}
	row.CreatedBy = email
	row.TestStatus = "untested"
	row.SHA256 = ""
	row.SizeBytes = 0
	if err := normalizeImage(&row, true, h.cfg.ImageStorageDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(id, email, "image.create", "image", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) uploadImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.cfg.ImageUploadMaxBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "image file is required"})
		return
	}
	defer file.Close()

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	}
	architecture := strings.TrimSpace(c.PostForm("architecture"))
	if architecture == "" {
		architecture = "x86_64"
	}
	status := strings.TrimSpace(c.PostForm("status"))
	if status == "" {
		status = "enabled"
	}
	imageMeta := models.Image{Name: name, OSFamily: c.PostForm("os_family"), OSVersion: c.PostForm("os_version"), Architecture: architecture, Status: status}
	if rawTags := strings.TrimSpace(c.PostForm("tags")); rawTags != "" {
		imageMeta.Tags = datatypes.JSON([]byte(rawTags))
	}
	if err := normalizeImage(&imageMeta, false, h.cfg.ImageStorageDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(h.cfg.ImageStorageDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), sanitizeUploadName(header.Filename))
	path, err := config.ResolveImageStoragePath(h.cfg.ImageStorageDir, filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer dst.Close()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(dst, hash), file)
	if err != nil {
		_ = os.Remove(path)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if size == 0 {
		_ = os.Remove(path)
		c.JSON(http.StatusBadRequest, gin.H{"error": "image file is empty"})
		return
	}

	id, email := middleware.Actor(c)
	row := models.Image{
		Name:         imageMeta.Name,
		OSFamily:     imageMeta.OSFamily,
		OSVersion:    imageMeta.OSVersion,
		Architecture: imageMeta.Architecture,
		FilePath:     path,
		SizeBytes:    size,
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
		Status:       imageMeta.Status,
		TestStatus:   "tested_passed",
		Tags:         imageMeta.Tags,
		CreatedBy:    email,
	}
	if err := h.db.Create(&row).Error; err != nil {
		_ = os.Remove(path)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(id, email, "image.upload", "image", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, row)
}

func (h Handler) updateImage(c *gin.Context) {
	var row models.Image
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var req map[string]json.RawMessage
	if !bind(c, &req) {
		return
	}
	filePathChanged, err := applyImagePatch(&row, req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeImage(&row, true, h.cfg.ImageStorageDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if filePathChanged {
		row.TestStatus = "untested"
		row.SHA256 = ""
		row.SizeBytes = 0
	}
	if err := h.db.Save(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "image.update", "image", row.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, row)
}

func (h Handler) deleteImage(c *gin.Context) {
	var row models.Image
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	var deployments int64
	if err := h.db.Model(&models.Deployment{}).Where("image_id = ?", row.ID).Count(&deployments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if deployments > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("image is referenced by %d deployment(s); disable it instead of deleting", deployments)})
		return
	}
	if err := h.db.Delete(&row).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "image.delete", "image", c.Param("id"), "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.Status(http.StatusNoContent)
}

func (h Handler) verifyImage(c *gin.Context) {
	image, err := h.images.Verify(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "test_status": image.TestStatus})
		return
	}
	id, email := middleware.Actor(c)
	h.audit.Record(id, email, "image.verify", "image", c.Param("id"), "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, image)
}

func normalizeImage(row *models.Image, requireFilePath bool, storageDir string) error {
	row.Name = strings.TrimSpace(row.Name)
	row.FilePath = strings.TrimSpace(row.FilePath)
	row.OSFamily = strings.TrimSpace(row.OSFamily)
	row.OSVersion = strings.TrimSpace(row.OSVersion)
	row.Architecture = strings.TrimSpace(row.Architecture)
	row.Status = strings.TrimSpace(row.Status)
	if row.Status == "" {
		row.Status = "enabled"
	}
	if row.Architecture == "" {
		row.Architecture = "x86_64"
	}
	if row.Name == "" {
		return fmt.Errorf("name is required")
	}
	if requireFilePath && row.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if row.FilePath != "" {
		resolvedPath, err := config.ResolveImageStoragePath(storageDir, row.FilePath)
		if err != nil {
			return err
		}
		row.FilePath = resolvedPath
	}
	if !stringIn(row.Status, "enabled", "disabled") {
		return fmt.Errorf("status must be one of enabled, disabled")
	}
	if !stringIn(row.Architecture, "x86_64", "arm64") {
		return fmt.Errorf("architecture must be x86_64 or arm64")
	}
	tags, err := normalizeOptionalJSON(row.Tags, "tags")
	if err != nil {
		return err
	}
	row.Tags = tags
	return nil
}

func applyImagePatch(row *models.Image, req map[string]json.RawMessage) (bool, error) {
	filePathChanged := false
	for key, raw := range req {
		switch key {
		case "id", "created_by", "created_at", "updated_at", "deleted_at", "test_status", "sha256", "size_bytes":
			continue
		case "name":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.Name = value
		case "os_family":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.OSFamily = value
		case "os_version":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.OSVersion = value
		case "architecture":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.Architecture = value
		case "file_path":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.FilePath = value
			filePathChanged = true
		case "status":
			value, err := stringFromJSON(raw, key)
			if err != nil {
				return false, err
			}
			row.Status = value
		case "tags":
			row.Tags = jsonFromRaw(raw)
		default:
			return false, fmt.Errorf("unsupported field %q", key)
		}
	}
	return filePathChanged, nil
}

func sanitizeUploadName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "" {
		return "image.bin"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
}
