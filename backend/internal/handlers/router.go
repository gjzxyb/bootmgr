package handlers

import (
	"context"
	"net/http"
	"time"

	"baremetal-platform/backend/internal/cache"
	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db            *gorm.DB
	cfg           config.Config
	redis         cache.RedisClient
	auth          services.AuthService
	audit         services.AuditService
	workflow      services.WorkflowService
	boot          services.BootService
	bmc           services.BMCService
	collector     services.CollectorService
	credentials   services.CredentialService
	images        services.ImageService
	bom           services.BOMService
	scripts       services.ScriptService
	terminals     services.TerminalService
	sshExecutor   services.SSHCommandExecutor
	statusHistory services.StatusHistoryService
	loginLimiter  *loginRateLimiter
}

func NewRouter(db *gorm.DB, cfg config.Config) *gin.Engine {
	statusHistory := services.NewStatusHistoryService(db)
	sshExecutor := services.NewSSHCommandExecutor(db, cfg)
	h := Handler{db: db, cfg: cfg, redis: cache.NewRedisClient(cfg), auth: services.NewAuthService(db, cfg), audit: services.NewAuditService(db), workflow: services.NewWorkflowService(db, statusHistory, cfg.DeploymentConcurrency), boot: services.NewBootService(db, cfg), bmc: services.NewBMCService(db, cfg), collector: services.NewCollectorService(db, cfg), credentials: services.NewCredentialService(db, cfg), images: services.NewImageService(db, cfg), bom: services.NewBOMService(db), scripts: services.NewScriptService(db, cfg), terminals: services.NewTerminalService(db, cfg), sshExecutor: sshExecutor, statusHistory: statusHistory, loginLimiter: newLoginRateLimiter(cfg.LoginRateAttempts, cfg.LoginRateWindow)}
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.AccessLog(), gin.Recovery())
	corsOrigins := cfg.CORSAllowedOrigins
	if len(corsOrigins) == 0 {
		corsOrigins = config.DefaultCORSAllowedOrigins()
	}
	r.Use(cors.New(cors.Config{AllowOrigins: corsOrigins, AllowMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"}, AllowHeaders: []string{"Authorization", "Content-Type", "X-Confirm-Action", "X-Request-ID"}, ExposeHeaders: []string{"X-Request-ID"}}))
	r.GET("/healthz", h.healthz)
	r.GET("/readyz", h.readyz)
	r.GET("/boot/ipxe", h.renderIPXE)
	r.GET("/boot/discovery.ipxe", h.discoveryIPXE)
	r.GET("/boot/linux-installer.ipxe", h.linuxInstallerIPXE)
	r.POST("/boot/events", h.recordBootEvent)
	r.GET("/metadata/instance-id", h.metadataInstanceIDByClientIP)
	r.GET("/metadata/hostname", h.metadataHostnameByClientIP)
	r.GET("/metadata/network", h.metadataNetworkByClientIP)
	r.GET("/metadata/ssh-keys", h.metadataSSHKeysByClientIP)
	r.GET("/metadata/userdata", h.metadataUserdataByClientIP)
	r.GET("/userdata", h.metadataUserdataByClientIP)
	r.GET("/metadata/by-server/:id/instance-id", h.metadataInstanceID)
	r.GET("/metadata/by-server/:id/hostname", h.metadataHostname)
	r.GET("/metadata/by-server/:id/network", h.metadataNetwork)
	r.GET("/metadata/by-server/:id/ssh-keys", h.metadataSSHKeys)
	r.GET("/metadata/by-server/:id/userdata", h.metadataUserdata)
	r.GET("/userdata/by-server/:id", h.metadataUserdata)
	r.GET("/metadata/by-token/:token/instance-id", h.metadataInstanceIDByToken)
	r.GET("/metadata/by-token/:token/hostname", h.metadataHostnameByToken)
	r.GET("/metadata/by-token/:token/network", h.metadataNetworkByToken)
	r.GET("/metadata/by-token/:token/ssh-keys", h.metadataSSHKeysByToken)
	r.GET("/metadata/by-token/:token/userdata", h.metadataUserdataByToken)
	r.GET("/userdata/by-token/:token", h.metadataUserdataByToken)
	r.GET("/metadata/by-mac/:mac/:field", h.metadataByMAC)
	r.GET("/userdata/by-mac/:mac", h.metadataUserdataByMAC)
	r.GET("/metadata/by-ip/:ip/:field", h.metadataByIP)
	r.GET("/userdata/by-ip/:ip", h.metadataUserdataByIP)
	r.GET("/metadata/by-deployment/:id/:field", h.metadataByDeployment)
	r.GET("/userdata/by-deployment/:id", h.metadataUserdataByDeployment)
	r.GET("/images/:id/file", h.serveImageFile)

	v1 := r.Group("/api/v1")
	v1.POST("/auth/login", h.login)

	protected := v1.Group("")
	protected.Use(middleware.Auth(h.auth))
	protected.GET("/auth/me", h.me)
	protected.GET("/dashboard", h.dashboard)

	adminOrOps := middleware.RequireRole("admin", "operator")
	adminOnly := middleware.RequireRole("admin")

	protected.GET("/users", adminOnly, h.listUsers)
	protected.POST("/users", adminOnly, h.createUser)
	protected.PATCH("/users/:id", adminOnly, h.updateUser)
	protected.POST("/users/:id/reset-password", adminOnly, h.resetUserPassword)
	protected.GET("/tenants", adminOnly, h.listTenants)
	protected.POST("/tenants", adminOnly, h.createTenant)
	protected.PATCH("/tenants/:id", adminOnly, h.updateTenant)
	protected.GET("/network-configs", h.listNetworkConfigs)
	protected.POST("/network-configs", adminOnly, h.createNetworkConfig)
	protected.POST("/network-configs/:id/check", adminOnly, h.checkNetworkConfig)
	protected.PATCH("/network-configs/:id", adminOnly, h.updateNetworkConfig)

	protected.GET("/servers", h.listServers)
	protected.POST("/servers", adminOrOps, h.createServer)
	protected.POST("/servers/import", adminOrOps, h.importServers)
	protected.POST("/servers/bmc/batch-power", adminOrOps, h.batchPower)
	protected.GET("/servers/:id", h.getServer)
	protected.PATCH("/servers/:id", adminOrOps, h.updateServer)
	protected.DELETE("/servers/:id", adminOnly, middleware.RequireConfirmation("server.delete"), h.deleteServer)
	protected.POST("/servers/:id/retire", adminOrOps, middleware.RequireConfirmation("server.retire"), h.retireServer)
	protected.POST("/servers/:id/scrap", adminOrOps, middleware.RequireConfirmation("server.scrap"), h.scrapServer)
	protected.GET("/servers/:id/inventory", h.getInventory)
	protected.GET("/servers/:id/status-history", h.listServerStatusHistory)
	protected.GET("/servers/:id/retirement-records", h.listServerRetirementRecords)
	protected.POST("/servers/:id/inventory", adminOrOps, h.createInventory)
	protected.GET("/servers/:id/bom", h.getServerBOM)
	protected.GET("/servers/:id/bom.csv", h.exportServerBOMCSV)
	protected.GET("/bom.csv", h.exportBOMCSV)

	protected.POST("/servers/:id/bmc", adminOnly, middleware.RequireConfirmation("bmc.upsert"), h.upsertBMC)
	protected.GET("/servers/:id/bmc/power", h.getPower)
	protected.GET("/servers/:id/bmc/firmware", h.getBMCFirmware)
	protected.POST("/servers/:id/bmc/power-on", adminOrOps, middleware.RequireConfirmation("bmc.power-on"), h.powerOn)
	protected.POST("/servers/:id/bmc/power-off", adminOrOps, middleware.RequireConfirmation("bmc.power-off"), h.powerOff)
	protected.POST("/servers/:id/bmc/reboot", adminOrOps, middleware.RequireConfirmation("bmc.reboot"), h.reboot)
	protected.POST("/servers/:id/bmc/check", adminOrOps, h.checkBMC)

	protected.GET("/images", h.listImages)
	protected.POST("/images", adminOrOps, h.createImage)
	protected.POST("/images/upload", adminOrOps, h.uploadImage)
	protected.PATCH("/images/:id", adminOrOps, h.updateImage)
	protected.DELETE("/images/:id", adminOnly, middleware.RequireConfirmation("image.delete"), h.deleteImage)
	protected.POST("/images/:id/verify", adminOrOps, h.verifyImage)
	protected.GET("/install-templates", h.listInstallTemplates)
	protected.POST("/install-templates", adminOrOps, h.createInstallTemplate)
	protected.PATCH("/install-templates/:id", adminOrOps, h.updateInstallTemplate)
	protected.DELETE("/install-templates/:id", adminOnly, middleware.RequireConfirmation("install_template.delete"), h.deleteInstallTemplate)
	protected.GET("/workflow-templates", h.listWorkflowTemplates)
	protected.POST("/workflow-templates", adminOrOps, h.createWorkflowTemplate)
	protected.PATCH("/workflow-templates/:id", adminOrOps, h.updateWorkflowTemplate)
	protected.DELETE("/workflow-templates/:id", adminOnly, middleware.RequireConfirmation("workflow_template.delete"), h.deleteWorkflowTemplate)

	protected.GET("/deployments", h.listDeployments)
	protected.POST("/deployments", adminOrOps, middleware.RequireConfirmation("deployment.create"), h.createDeployment)
	protected.POST("/deployments/batch", adminOrOps, middleware.RequireConfirmation("deployment.batch-create"), h.createDeploymentBatch)
	protected.GET("/deployments/:id", h.getDeployment)
	protected.POST("/deployments/:id/cancel", adminOrOps, middleware.RequireConfirmation("deployment.cancel"), h.cancelDeployment)
	protected.POST("/deployments/:id/retry", adminOrOps, middleware.RequireConfirmation("deployment.retry"), h.retryDeployment)
	protected.GET("/deployments/:id/logs", h.deploymentLogs)

	protected.GET("/servers/:id/metrics", h.serverMetrics)
	protected.POST("/servers/:id/ssh", adminOnly, middleware.RequireConfirmation("ssh.upsert"), h.upsertSSHAccess)
	protected.GET("/servers/:id/ssh", h.getSSHAccess)
	protected.POST("/servers/:id/collections", adminOrOps, h.startCollection)
	protected.GET("/servers/:id/collections", h.listCollections)
	protected.GET("/collections", h.listCollectionJobs)
	protected.GET("/log-events", h.listLogEvents)
	protected.GET("/ops/script-jobs", h.listScriptJobs)
	protected.POST("/ops/script-jobs", adminOrOps, middleware.RequireConfirmation("ops.script.create"), h.createScriptJob)
	protected.GET("/ops/script-jobs/:id", h.getScriptJob)
	protected.GET("/ops/script-jobs/:id/results", h.listScriptExecutions)
	protected.POST("/ops/log-collections", adminOrOps, middleware.RequireConfirmation("ops.logs.collect"), h.collectLogs)
	protected.GET("/ops/terminal-sessions", h.listTerminalSessions)
	protected.POST("/ops/terminal-sessions", adminOrOps, middleware.RequireConfirmation("ops.terminal.open"), h.openTerminalSession)
	protected.GET("/ops/terminal-sessions/:id", h.getTerminalSession)
	protected.POST("/ops/terminal-sessions/:id/commands", adminOrOps, middleware.RequireConfirmation("ops.terminal.command"), h.runTerminalCommand)
	protected.POST("/ops/terminal-sessions/:id/close", adminOrOps, middleware.RequireConfirmation("ops.terminal.close"), h.closeTerminalSession)
	protected.GET("/ops/backup/export", adminOnly, middleware.RequireConfirmation("ops.backup.export"), h.exportBackup)
	protected.POST("/ops/backup/validate", adminOnly, h.validateBackup)
	protected.POST("/ops/backup/restore", adminOnly, middleware.RequireConfirmation("ops.backup.restore"), h.restoreBackup)
	protected.GET("/alerts", h.listAlerts)
	protected.GET("/alert-rules", h.listAlertRules)
	protected.POST("/alert-rules", adminOrOps, h.createAlertRule)
	protected.PATCH("/alert-rules/:id", adminOrOps, h.updateAlertRule)
	protected.POST("/alert-rules/evaluate", adminOrOps, h.evaluateAlertRules)
	protected.POST("/alerts/:id/ack", h.ackAlert)
	protected.POST("/alerts/:id/resolve", h.resolveAlert)
	protected.GET("/alerts/:id/events", h.listAlertEvents)

	protected.GET("/audit-logs", h.listAuditLogs)
	protected.GET("/audit-logs/:id", h.getAuditLog)
	return r
}

func (h Handler) healthz(c *gin.Context) {
	sqlDB, err := h.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": "error", "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": "error", "error": err.Error()})
		return
	}
	redisStatus := "disabled"
	if h.redis.Enabled() {
		redisStatus = "ok"
		if err := h.redis.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": "ok", "redis": "error", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok", "redis": redisStatus})
}

func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func notFound(c *gin.Context, err error) bool {
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return true
	}
	return false
}
