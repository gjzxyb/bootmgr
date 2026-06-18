package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"

	"github.com/gin-gonic/gin"
)

func (h Handler) listScriptJobs(c *gin.Context) {
	query := h.db.Model(&models.ScriptJob{})
	if value := strings.TrimSpace(c.Query("keyword")); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR script LIKE ?", like, like)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("requested_by"); value != "" {
		query = query.Where("requested_by = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.ScriptJob
		query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.ScriptJob
	query.Order("created_at desc").Limit(100).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) createScriptJob(c *gin.Context) {
	var req struct {
		Name           string `json:"name"`
		Script         string `json:"script" binding:"required"`
		ServerIDs      []uint `json:"server_ids" binding:"required"`
		Concurrency    int    `json:"concurrency"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if !bind(c, &req) {
		return
	}
	_, email := middleware.Actor(c)
	job, err := h.scripts.CreateJob(req.Name, req.Script, email, req.ServerIDs, req.Concurrency, req.TimeoutSeconds)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, actor := middleware.Actor(c)
	h.audit.Record(id, actor, "ops.script.create", "script_job", job.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, job)
}

func (h Handler) getScriptJob(c *gin.Context) {
	var row models.ScriptJob
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h Handler) listScriptExecutions(c *gin.Context) {
	var job models.ScriptJob
	if notFound(c, h.db.First(&job, c.Param("id")).Error) {
		return
	}
	var rows []models.ScriptExecution
	h.db.Where("script_job_id = ?", c.Param("id")).Order("id asc").Find(&rows)
	c.JSON(http.StatusOK, rows)
}

type logCollectionResult struct {
	ServerID uint   `json:"server_id"`
	Status   string `json:"status"`
	Events   []uint `json:"events,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (h Handler) collectLogs(c *gin.Context) {
	var req struct {
		ServerIDs []uint   `json:"server_ids" binding:"required"`
		Sources   []string `json:"sources"`
	}
	if !bind(c, &req) {
		return
	}
	serverIDs, err := normalizeOpsServerIDs(req.ServerIDs, 100)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sources, err := normalizeLogSources(req.Sources)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	now := time.Now().UTC()
	results := make([]logCollectionResult, 0, len(serverIDs))
	succeeded := 0
	eventsCreated := 0
	for _, serverID := range serverIDs {
		result := h.collectServerLogs(serverID, sources, actorEmail, c.GetString("request_id"), now)
		if result.Status == "success" {
			succeeded++
			eventsCreated += len(result.Events)
		}
		h.audit.Record(actorID, actorEmail, "ops.logs.collect", "server", serverID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
		results = append(results, result)
	}
	c.JSON(http.StatusCreated, gin.H{"requested": len(serverIDs), "succeeded": succeeded, "failed": len(serverIDs) - succeeded, "events_created": eventsCreated, "results": results})
}

func (h Handler) collectServerLogs(serverID uint, sources []string, actorEmail string, requestID string, now time.Time) logCollectionResult {
	result := logCollectionResult{ServerID: serverID, Status: "failed"}
	var server models.Server
	if err := h.db.First(&server, serverID).Error; err != nil {
		result.Error = "server not found"
		return result
	}
	if terminalServerStatus(server.Status) {
		result.Error = "server is not eligible for log collection"
		return result
	}
	if strings.ToLower(strings.TrimSpace(h.cfg.SSHOperationsMode)) == "ssh" {
		return h.collectServerLogsSSH(server, sources, requestID, now)
	}
	for _, source := range sources {
		event := models.LogEvent{ServerID: serverID, Source: source, Level: logCollectionLevel(source), Message: logCollectionMessage(source, server, actorEmail), TraceID: requestID, OccurredAt: now}
		if err := h.db.Create(&event).Error; err != nil {
			result.Error = err.Error()
			return result
		}
		result.Events = append(result.Events, event.ID)
	}
	result.Status = "success"
	return result
}

func (h Handler) collectServerLogsSSH(server models.Server, sources []string, requestID string, now time.Time) logCollectionResult {
	result := logCollectionResult{ServerID: server.ID, Status: "failed"}
	for _, source := range sources {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := sshLogCommand(source)
		remoteResult, err := h.sshExecutor.RunForServer(ctx, server.ID, command)
		cancel()
		if err != nil {
			result.Error = fmt.Sprintf("ssh %s log collection failed: %v", source, err)
			if strings.TrimSpace(remoteResult.Stderr) != "" {
				result.Error += ": " + strings.TrimSpace(remoteResult.Stderr)
			}
			return result
		}
		message := fmt.Sprintf("SSH %s log collection for %s\n%s", source, displayServerName(server), trimLogPayload(remoteResult.Stdout, 4000))
		if strings.TrimSpace(remoteResult.Stderr) != "" {
			message += "\nstderr:\n" + trimLogPayload(remoteResult.Stderr, 1000)
		}
		event := models.LogEvent{ServerID: server.ID, Source: source, Level: logCollectionLevel(source), Message: message, TraceID: requestID, OccurredAt: now}
		if err := h.db.Create(&event).Error; err != nil {
			result.Error = err.Error()
			return result
		}
		result.Events = append(result.Events, event.ID)
	}
	result.Status = "success"
	return result
}

func sshLogCommand(source string) string {
	switch source {
	case "syslog":
		return "if [ -r /var/log/syslog ]; then tail -n 120 /var/log/syslog; elif command -v journalctl >/dev/null 2>&1; then journalctl -n 120 --no-pager; elif [ -r /var/log/messages ]; then tail -n 120 /var/log/messages; else echo 'no syslog source found'; fi"
	case "dmesg":
		return "dmesg 2>/dev/null | tail -n 120 || echo 'dmesg unavailable'"
	case "hardware":
		return "printf 'hostname: '; hostname; printf 'kernel: '; uname -a; if command -v lscpu >/dev/null 2>&1; then lscpu | head -n 40; fi; if command -v lsblk >/dev/null 2>&1; then lsblk -o NAME,SIZE,TYPE,MOUNTPOINT | head -n 40; fi; if command -v ip >/dev/null 2>&1; then ip -brief addr; fi"
	default:
		return "echo unsupported log source"
	}
}

func trimLogPayload(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "\n<truncated>"
}

func displayServerName(server models.Server) string {
	if strings.TrimSpace(server.Hostname) != "" {
		return server.Hostname
	}
	if strings.TrimSpace(server.AssetNo) != "" {
		return server.AssetNo
	}
	return fmt.Sprintf("server-%d", server.ID)
}

func normalizeOpsServerIDs(ids []uint, limit int) ([]uint, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("server_ids cannot be empty")
	}
	if len(ids) > limit {
		return nil, fmt.Errorf("cannot target more than %d servers", limit)
	}
	seen := map[uint]bool{}
	normalized := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, fmt.Errorf("server_ids cannot contain 0")
		}
		if seen[id] {
			return nil, fmt.Errorf("server_ids contains duplicate server %d", id)
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func normalizeLogSources(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"syslog", "dmesg", "hardware"}, nil
	}
	allowed := map[string]bool{"syslog": true, "dmesg": true, "hardware": true}
	seen := map[string]bool{}
	sources := make([]string, 0, len(raw))
	for _, source := range raw {
		source = strings.ToLower(strings.TrimSpace(source))
		if !allowed[source] {
			return nil, fmt.Errorf("sources must contain only syslog, dmesg, hardware")
		}
		if seen[source] {
			return nil, fmt.Errorf("sources cannot contain duplicates")
		}
		seen[source] = true
		sources = append(sources, source)
	}
	return sources, nil
}

func logCollectionLevel(source string) string {
	if source == "dmesg" {
		return "warning"
	}
	return "info"
}

func logCollectionMessage(source string, server models.Server, actorEmail string) string {
	hostname := server.Hostname
	if hostname == "" {
		hostname = fmt.Sprintf("server-%d", server.ID)
	}
	switch source {
	case "syslog":
		return fmt.Sprintf("Simulated syslog collection captured service and auth summaries for %s by %s", hostname, actorEmail)
	case "dmesg":
		return fmt.Sprintf("Simulated dmesg collection captured kernel, driver, and boot diagnostics for %s by %s", hostname, actorEmail)
	case "hardware":
		return fmt.Sprintf("Simulated hardware log collection captured SMART, PCI, and inventory summaries for %s by %s", hostname, actorEmail)
	default:
		return fmt.Sprintf("Simulated log collection captured %s logs for %s by %s", source, hostname, actorEmail)
	}
}

func (h Handler) listTerminalSessions(c *gin.Context) {
	if !h.cleanupExpiredTerminalSessions(c) {
		return
	}
	query := h.db.Model(&models.TerminalSession{})
	if value := c.Query("server_id"); value != "" {
		query = query.Where("server_id = ?", value)
	}
	if value := c.Query("status"); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := c.Query("mode"); value != "" {
		query = query.Where("mode = ?", value)
	}
	if value := c.Query("requested_by"); value != "" {
		query = query.Where("requested_by = ?", value)
	}
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page := positiveInt(c.Query("page"), 1)
		pageSize := positiveInt(c.Query("page_size"), 20)
		if pageSize > 100 {
			pageSize = 100
		}
		var total int64
		query.Count(&total)
		var rows []models.TerminalSession
		query.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
		return
	}
	var rows []models.TerminalSession
	query.Order("created_at desc").Limit(100).Find(&rows)
	c.JSON(http.StatusOK, rows)
}

func (h Handler) openTerminalSession(c *gin.Context) {
	var req struct {
		ServerID uint   `json:"server_id" binding:"required"`
		Reason   string `json:"reason"`
	}
	if !bind(c, &req) {
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	session, err := h.terminals.Open(req.ServerID, actorEmail, req.Reason)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(actorID, actorEmail, "ops.terminal.open", "terminal_session", session.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, session)
}

func (h Handler) getTerminalSession(c *gin.Context) {
	if !h.cleanupExpiredTerminalSessions(c) {
		return
	}
	var row models.TerminalSession
	if notFound(c, h.db.First(&row, c.Param("id")).Error) {
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h Handler) closeTerminalSession(c *gin.Context) {
	sessionID := uintFromParam(c.Param("id"))
	if sessionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid terminal session id"})
		return
	}
	session, err := h.terminals.Close(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "ops.terminal.close", "terminal_session", session.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, session)
}

func (h Handler) runTerminalCommand(c *gin.Context) {
	sessionID := uintFromParam(c.Param("id"))
	if sessionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid terminal session id"})
		return
	}
	var req struct {
		Command string `json:"command" binding:"required"`
	}
	if !bind(c, &req) {
		return
	}
	actorID, actorEmail := middleware.Actor(c)
	session, err := h.terminals.RunCommand(sessionID, actorEmail, req.Command)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.audit.Record(actorID, actorEmail, "ops.terminal.command", "terminal_session", session.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, session)
}

func (h Handler) cleanupExpiredTerminalSessions(c *gin.Context) bool {
	sessions, err := h.terminals.CloseExpired(time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "terminal session cleanup failed"})
		return false
	}
	for _, session := range sessions {
		h.audit.Record(0, "system", "ops.terminal.auto_close", "terminal_session", session.ID, "high", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	}
	return true
}

func uintFromParam(value string) uint {
	var result uint
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0
		}
		result = result*10 + uint(value[i]-'0')
	}
	return result
}
