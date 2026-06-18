package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

const defaultTerminalSessionTTL = 60 * time.Minute

type TerminalService struct {
	db       *gorm.DB
	ttl      time.Duration
	mode     string
	executor SSHCommandExecutor
}

func NewTerminalService(db *gorm.DB, cfg config.Config) TerminalService {
	ttl := cfg.TerminalSessionTTL
	if ttl <= 0 {
		ttl = defaultTerminalSessionTTL
	}
	return TerminalService{db: db, ttl: ttl, mode: strings.ToLower(strings.TrimSpace(cfg.SSHOperationsMode)), executor: NewSSHCommandExecutor(db, cfg)}
}

func (s TerminalService) Open(serverID uint, requestedBy, reason string) (models.TerminalSession, error) {
	if serverID == 0 {
		return models.TerminalSession{}, fmt.Errorf("server_id is required")
	}
	var server models.Server
	if err := s.db.First(&server, serverID).Error; err != nil {
		return models.TerminalSession{}, fmt.Errorf("server not found")
	}
	if server.Status == "retired" || server.Status == "scrapped" {
		return models.TerminalSession{}, fmt.Errorf("server is not eligible for terminal sessions")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 255 {
		return models.TerminalSession{}, fmt.Errorf("reason must be 255 characters or fewer")
	}
	if reason == "" {
		reason = "manual operation"
	}
	now := time.Now().UTC()
	mode := "simulated"
	transcript := fmt.Sprintf("Simulated terminal session opened for server %d (%s).\nNo real SSH connection is established in simulated mode.", server.ID, server.Hostname)
	if s.mode == "ssh" {
		mode = "ssh"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := s.executor.RunForServer(ctx, server.ID, "printf 'SSH terminal session opened on '; hostname; printf 'user='; id -un; uname -a")
		transcript = fmt.Sprintf("SSH terminal session opened for server %d (%s).\n%s", server.ID, server.Hostname, strings.TrimSpace(result.Stdout))
		if strings.TrimSpace(result.Stderr) != "" {
			transcript = appendTerminalTranscript(transcript, "stderr:\n"+strings.TrimSpace(result.Stderr))
		}
		if err != nil {
			return models.TerminalSession{}, fmt.Errorf("open ssh terminal validation failed: %w", err)
		}
	}
	session := models.TerminalSession{
		ServerID:    server.ID,
		Status:      "active",
		Mode:        mode,
		RequestedBy: requestedBy,
		Reason:      reason,
		Transcript:  transcript,
		OpenedAt:    now,
	}
	return session, s.db.Create(&session).Error
}

func (s TerminalService) RunCommand(sessionID uint, requestedBy, command string) (models.TerminalSession, error) {
	var session models.TerminalSession
	if err := s.db.First(&session, sessionID).Error; err != nil {
		return session, err
	}
	if session.Status != "active" {
		return session, fmt.Errorf("terminal session is not active")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return session, fmt.Errorf("command cannot be empty")
	}
	if len([]rune(command)) > 4000 {
		return session, fmt.Errorf("command cannot exceed 4000 characters")
	}
	line := fmt.Sprintf("[%s] %s$ %s", time.Now().UTC().Format(time.RFC3339), requestedBy, command)
	transcript := appendTerminalTranscript(session.Transcript, line)
	if session.Mode == "ssh" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := s.executor.RunForServer(ctx, session.ServerID, command)
		if strings.TrimSpace(result.Stdout) != "" {
			transcript = appendTerminalTranscript(transcript, strings.TrimSpace(result.Stdout))
		}
		if strings.TrimSpace(result.Stderr) != "" {
			transcript = appendTerminalTranscript(transcript, "stderr:\n"+strings.TrimSpace(result.Stderr))
		}
		if err != nil || result.ExitCode != 0 {
			transcript = appendTerminalTranscript(transcript, fmt.Sprintf("exit_code=%d", result.ExitCode))
		}
		if err != nil {
			transcript = appendTerminalTranscript(transcript, "error: "+err.Error())
		}
	} else {
		transcript = appendTerminalTranscript(transcript, "Simulated command accepted; no remote SSH command was executed.")
	}
	if err := s.db.Model(&session).Update("transcript", transcript).Error; err != nil {
		return session, err
	}
	session.Transcript = transcript
	return session, nil
}

func (s TerminalService) Close(sessionID uint) (models.TerminalSession, error) {
	var session models.TerminalSession
	if err := s.db.First(&session, sessionID).Error; err != nil {
		return session, err
	}
	if session.Status == "closed" {
		return session, nil
	}
	now := time.Now().UTC()
	transcript := appendTerminalTranscript(session.Transcript, "Terminal session closed by operator.")
	err := s.db.Model(&session).Updates(map[string]any{"status": "closed", "closed_at": now, "transcript": transcript}).Error
	if err != nil {
		return session, err
	}
	session.Status = "closed"
	session.ClosedAt = &now
	session.Transcript = transcript
	return session, nil
}

func (s TerminalService) CloseExpired(now time.Time) ([]models.TerminalSession, error) {
	if s.ttl <= 0 {
		return nil, nil
	}
	now = now.UTC()
	cutoff := now.Add(-s.ttl)
	var active []models.TerminalSession
	if err := s.db.Where("status = ? AND opened_at <= ?", "active", cutoff).Order("id asc").Find(&active).Error; err != nil {
		return nil, err
	}
	closed := make([]models.TerminalSession, 0, len(active))
	message := fmt.Sprintf("Terminal session auto-closed after %d minute TTL.", int(s.ttl/time.Minute))
	for _, session := range active {
		transcript := appendTerminalTranscript(session.Transcript, message)
		tx := s.db.Model(&models.TerminalSession{}).Where("id = ? AND status = ?", session.ID, "active").Updates(map[string]any{"status": "closed", "closed_at": now, "transcript": transcript})
		if tx.Error != nil {
			return closed, tx.Error
		}
		if tx.RowsAffected == 0 {
			continue
		}
		session.Status = "closed"
		session.ClosedAt = &now
		session.Transcript = transcript
		closed = append(closed, session)
	}
	return closed, nil
}

func (s TerminalService) TTL() time.Duration {
	if s.ttl <= 0 {
		return defaultTerminalSessionTTL
	}
	return s.ttl
}

func appendTerminalTranscript(transcript string, line string) string {
	if strings.TrimSpace(transcript) == "" {
		return line
	}
	if strings.HasSuffix(transcript, "\n") {
		return transcript + line
	}
	return transcript + "\n" + line
}
