package handlers

import (
	"context"
	"net/http"
	"time"

	"baremetal-platform/backend/internal/config"

	"github.com/gin-gonic/gin"
)

type readinessCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type readinessResponse struct {
	Status       string           `json:"status"`
	Checks       []readinessCheck `json:"checks"`
	ConfigIssues []config.Issue   `json:"config_issues"`
}

func (h Handler) readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	response := readinessResponse{Status: "ok", Checks: []readinessCheck{}, ConfigIssues: []config.Issue{}}
	addCheck := func(name string, status string, message string) {
		if status != "ok" {
			response.Status = "degraded"
		}
		response.Checks = append(response.Checks, readinessCheck{Name: name, Status: status, Message: message})
	}

	if h.db == nil {
		addCheck("database", "error", "database handle is not initialized")
	} else if sqlDB, err := h.db.DB(); err != nil {
		addCheck("database", "error", err.Error())
	} else if err := sqlDB.PingContext(ctx); err != nil {
		addCheck("database", "error", err.Error())
	} else {
		addCheck("database", "ok", "database ping succeeded")
	}

	if !h.redis.Enabled() {
		if config.IsProduction(h.cfg.AppEnv) {
			addCheck("redis", "warning", "redis is not configured")
		} else {
			addCheck("redis", "ok", "redis is disabled")
		}
	} else if err := h.redis.Ping(ctx); err != nil {
		addCheck("redis", "error", err.Error())
	} else {
		addCheck("redis", "ok", "redis ping succeeded")
	}

	if err := config.CheckImageStorage(h.cfg.ImageStorageDir); err != nil {
		addCheck("image_storage", "error", err.Error())
	} else {
		addCheck("image_storage", "ok", "image storage is writable")
	}

	bootIssues := config.BootRuntimeIssues(h.cfg)
	if !h.cfg.BootServicesEnabled {
		if config.IsProduction(h.cfg.AppEnv) {
			addCheck("pxe_services", "warning", "PXE/DHCP/TFTP listeners are disabled")
		} else {
			addCheck("pxe_services", "ok", "PXE/DHCP/TFTP listeners are disabled")
		}
	} else {
		bootStatus := "ok"
		bootMessage := "PXE/DHCP/TFTP runtime checks passed"
		for _, issue := range bootIssues {
			if issue.Level == "error" {
				bootStatus = "error"
				bootMessage = issue.Message
				break
			}
			if issue.Level == "warning" && bootStatus == "ok" {
				bootStatus = "warning"
				bootMessage = issue.Message
			}
		}
		addCheck("pxe_services", bootStatus, bootMessage)
	}

	validation := config.Validate(h.cfg)
	response.ConfigIssues = validation.Issues()
	response.ConfigIssues = append(response.ConfigIssues, bootIssues...)
	if validation.HasErrors() {
		addCheck("config", "error", "configuration has blocking errors")
	} else if validation.HasWarnings() {
		addCheck("config", "warning", "configuration has warnings")
	} else {
		addCheck("config", "ok", "configuration passed validation")
	}

	c.JSON(http.StatusOK, response)
}
