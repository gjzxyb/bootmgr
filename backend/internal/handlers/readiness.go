package handlers

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/services"

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

	toolingStatus, toolingMessage := bmcToolingStatus(h.cfg.BMCAdapter)
	addCheck("bmc_tooling", toolingStatus, toolingMessage)

	sshKnownHostsStatus, sshKnownHostsMessage := h.sshKnownHostsStatus()
	addCheck("ssh_known_hosts", sshKnownHostsStatus, sshKnownHostsMessage)

	bootIssues := config.BootRuntimeIssues(h.cfg)
	pxeStatus, pxeMessage := pxeReadinessStatus(ctx, h.cfg, bootIssues)
	addCheck("pxe_services", pxeStatus, pxeMessage)

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

func pxeReadinessStatus(ctx context.Context, cfg config.Config, bootIssues []config.Issue) (string, string) {
	if !cfg.BootServicesEnabled {
		if config.IsProduction(cfg.AppEnv) {
			return "warning", "PXE/DHCP/TFTP listeners are disabled"
		}
		return "ok", "PXE/DHCP/TFTP listeners are disabled"
	}
	status := "ok"
	issueMessage := ""
	for _, issue := range bootIssues {
		if issue.Level == "error" {
			return "error", issue.Message
		}
		if issue.Level == "warning" && status == "ok" {
			status = "warning"
			issueMessage = issue.Message
		}
	}
	data, tftpOptions, err := services.ProbeTFTPFileWithOptions(ctx, cfg.BootTFTPListenAddr, "boot.ipxe", 64*1024, map[string]string{"blksize": "1024", "timeout": "1", "tsize": "0"})
	if err != nil {
		return "error", "TFTP boot.ipxe probe failed: " + err.Error()
	}
	if !strings.Contains(string(data), "#!ipxe") {
		return "error", "TFTP boot.ipxe did not return an iPXE script"
	}
	if tftpOptions["blksize"] == "" || tftpOptions["tsize"] == "" {
		return "error", "TFTP boot.ipxe probe did not negotiate blksize/tsize options"
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.BootServiceMode))
	if mode == "" {
		mode = "proxy"
	}
	tftpMessage := "TFTP OACK blksize=" + tftpOptions["blksize"] + " tsize=" + tftpOptions["tsize"]
	message := "PXE/DHCP/TFTP runtime probes passed; " + tftpMessage
	if mode == "external" {
		message = "PXE/TFTP runtime probe passed; external DHCP is expected; " + tftpMessage
	} else {
		probe, err := services.ProbePXEDHCP(ctx, cfg.BootDHCPListenAddr, "52:54:00:00:00:fe", 9)
		if err != nil {
			return "error", "DHCP/ProxyDHCP probe failed: " + err.Error()
		}
		message = "PXE/DHCP/TFTP runtime probes passed; " + tftpMessage + "; DHCP bootfile=" + probe.Bootfile
		if probe.LegacyBootfile != "" {
			message += " legacy_bootfile=" + probe.LegacyBootfile
		}
		if probe.ServerIP != "" {
			message += " server_identifier=" + probe.ServerIP
		}
		if probe.NextServerIP != "" {
			message += " next_server=" + probe.NextServerIP
		}
		if probe.TFTPServerName != "" {
			message += " tftp_server_name=" + probe.TFTPServerName
		}
		if mode == "proxy" {
			proxyAddr, err := services.ProxyDHCPBootServerListenAddr(cfg.BootDHCPListenAddr)
			if err != nil {
				return "error", "PXE Boot Server listen address is invalid: " + err.Error()
			}
			proxyProbeAddr := proxyAddr
			if host, port, splitErr := net.SplitHostPort(proxyAddr); splitErr == nil && (host == "0.0.0.0" || host == "::" || host == "") {
				proxyProbeAddr = net.JoinHostPort(strings.TrimSpace(cfg.BootDHCPServerIP), port)
			}
			proxyProbe, err := services.ProbePXEDHCP(ctx, proxyProbeAddr, "52:54:00:00:00:fd", 9)
			if err != nil {
				return "error", "PXE Boot Server probe failed: " + err.Error()
			}
			message += " proxy_boot_server=" + proxyAddr + " proxy_probe=" + proxyProbeAddr + " proxy_bootfile=" + proxyProbe.Bootfile
		}
	}
	if status == "warning" {
		return status, issueMessage + "; " + message
	}
	return status, message
}
