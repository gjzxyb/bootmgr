package handlers

import (
	"net/http"
	"os"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func (h Handler) serveImageFile(c *gin.Context) {
	var image models.Image
	if notFound(c, h.db.First(&image, c.Param("id")).Error) {
		return
	}
	if image.Status != "enabled" || image.TestStatus != "tested_passed" {
		c.JSON(http.StatusForbidden, gin.H{"error": "image is not enabled and verified"})
		return
	}
	path, err := config.ResolveExistingImageStoragePath(h.cfg.ImageStorageDir, image.FilePath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "image file is outside image storage or unavailable"})
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "image file not found"})
		return
	}
	c.Header("Accept-Ranges", "bytes")
	c.Header("X-Image-SHA256", image.SHA256)
	c.Header("X-Image-Size", formatInt(image.SizeBytes))
	http.ServeFile(c.Writer, c.Request, path)
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
