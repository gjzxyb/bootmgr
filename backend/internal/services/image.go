package services

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type ImageService struct {
	db         *gorm.DB
	storageDir string
}

func NewImageService(db *gorm.DB, cfg config.Config) ImageService {
	return ImageService{db: db, storageDir: cfg.ImageStorageDir}
}

func (s ImageService) Verify(imageID string) (models.Image, error) {
	var image models.Image
	if err := s.db.First(&image, imageID).Error; err != nil {
		return image, err
	}
	path, err := config.ResolveExistingImageStoragePath(s.storageDir, image.FilePath)
	if err != nil {
		s.db.Model(&image).Update("test_status", "test_failed")
		image.TestStatus = "test_failed"
		return image, err
	}
	file, err := os.Open(path)
	if err != nil {
		s.db.Model(&image).Update("test_status", "test_failed")
		image.TestStatus = "test_failed"
		return image, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		s.db.Model(&image).Update("test_status", "test_failed")
		image.TestStatus = "test_failed"
		return image, err
	}
	image.SHA256 = hex.EncodeToString(hash.Sum(nil))
	image.SizeBytes = size
	image.TestStatus = "tested_passed"
	image.FilePath = path
	if image.Status == "" {
		image.Status = "enabled"
	}
	return image, s.db.Model(&image).Updates(map[string]any{"file_path": image.FilePath, "sha256": image.SHA256, "size_bytes": image.SizeBytes, "test_status": image.TestStatus, "status": image.Status}).Error
}

func (s ImageService) Preflight(serverID, imageID uint, templateID *uint, workflowID *uint) []string {
	var problems []string
	var server models.Server
	if err := s.db.First(&server, serverID).Error; err != nil {
		problems = append(problems, "server not found")
	} else if !deployableServerStatus(server.Status) {
		problems = append(problems, "server status is not deployable: "+server.Status)
	}
	var networkCount int64
	if err := s.db.Model(&models.NetworkConfig{}).Where("purpose = ? AND status = ?", "deployment", "enabled").Count(&networkCount).Error; err != nil || networkCount == 0 {
		problems = append(problems, "deployment network not configured")
	}
	var image models.Image
	if err := s.db.First(&image, imageID).Error; err != nil {
		problems = append(problems, "image not found")
	} else {
		if image.Status != "enabled" {
			problems = append(problems, "image is not enabled")
		}
		if image.TestStatus != "tested_passed" {
			problems = append(problems, "image is not verified")
		}
	}
	if templateID != nil {
		var tmpl models.InstallTemplate
		if err := s.db.First(&tmpl, *templateID).Error; err != nil {
			problems = append(problems, "install template not found")
		} else if tmpl.Status != "enabled" {
			problems = append(problems, "install template is not enabled")
		}
	}
	if workflowID != nil {
		var tmpl models.WorkflowTemplate
		if err := s.db.First(&tmpl, *workflowID).Error; err != nil {
			problems = append(problems, "workflow template not found")
		} else if tmpl.Status != "enabled" {
			problems = append(problems, "workflow template is not enabled")
		}
	}
	return problems
}

func deployableServerStatus(status string) bool {
	return status == "ready" || status == "running" || status == "maintenance"
}
