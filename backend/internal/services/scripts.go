package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ScriptService struct {
	db       *gorm.DB
	mode     string
	executor SSHCommandExecutor
}

func NewScriptService(db *gorm.DB, cfg config.Config) ScriptService {
	return ScriptService{db: db, mode: strings.ToLower(strings.TrimSpace(cfg.SSHOperationsMode)), executor: NewSSHCommandExecutor(db, cfg)}
}

const simulatedScriptExecutionDuration = 200 * time.Millisecond

func (s ScriptService) CreateJob(name, script, requestedBy string, serverIDs []uint, concurrency, timeoutSeconds int) (models.ScriptJob, error) {
	name = strings.TrimSpace(name)
	script = strings.TrimSpace(script)
	if name == "" {
		name = "Ad-hoc script"
	}
	if script == "" {
		return models.ScriptJob{}, fmt.Errorf("script cannot be empty")
	}
	if len([]rune(script)) > 16000 {
		return models.ScriptJob{}, fmt.Errorf("script cannot exceed 16000 characters")
	}
	if len(serverIDs) == 0 {
		return models.ScriptJob{}, fmt.Errorf("server_ids cannot be empty")
	}
	if len(serverIDs) > 100 {
		return models.ScriptJob{}, fmt.Errorf("cannot target more than 100 servers in one script job")
	}
	if err := s.validateScriptTargets(serverIDs); err != nil {
		return models.ScriptJob{}, err
	}
	if concurrency <= 0 {
		concurrency = 5
	}
	if concurrency > 50 {
		return models.ScriptJob{}, fmt.Errorf("concurrency cannot exceed 50")
	}
	if concurrency > len(serverIDs) {
		concurrency = len(serverIDs)
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if timeoutSeconds > 3600 {
		return models.ScriptJob{}, fmt.Errorf("timeout_seconds cannot exceed 3600")
	}
	payload, _ := json.Marshal(serverIDs)
	now := time.Now().UTC()
	job := models.ScriptJob{Name: name, Script: script, ServerIDs: datatypes.JSON(payload), Status: "running", RequestedBy: requestedBy, Concurrency: concurrency, TimeoutSeconds: timeoutSeconds, StartedAt: &now}
	if err := s.db.Create(&job).Error; err != nil {
		return job, err
	}
	for _, serverID := range serverIDs {
		execution := models.ScriptExecution{ScriptJobID: job.ID, ServerID: serverID, Status: "pending"}
		if err := s.db.Create(&execution).Error; err != nil {
			return job, err
		}
	}
	go s.run(job.ID)
	return job, nil
}

func (s ScriptService) validateScriptTargets(serverIDs []uint) error {
	seen := map[uint]bool{}
	for _, serverID := range serverIDs {
		if serverID == 0 {
			return fmt.Errorf("server_ids cannot contain 0")
		}
		if seen[serverID] {
			return fmt.Errorf("server_ids contains duplicate server %d", serverID)
		}
		seen[serverID] = true
	}
	var servers []models.Server
	if err := s.db.Where("id IN ?", serverIDs).Find(&servers).Error; err != nil {
		return err
	}
	found := map[uint]models.Server{}
	for _, server := range servers {
		found[server.ID] = server
	}
	for _, serverID := range serverIDs {
		server, ok := found[serverID]
		if !ok {
			return fmt.Errorf("server %d not found", serverID)
		}
		if server.Status == "retired" || server.Status == "scrapped" {
			return fmt.Errorf("server %d status is not operable: %s", serverID, server.Status)
		}
	}
	return nil
}

func (s ScriptService) run(jobID uint) {
	var job models.ScriptJob
	if err := s.db.First(&job, jobID).Error; err != nil {
		return
	}
	var executions []models.ScriptExecution
	s.db.Where("script_job_id = ?", job.ID).Order("id asc").Find(&executions)
	concurrency := job.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	jobFailed := false
	for startIndex := 0; startIndex < len(executions); startIndex += concurrency {
		endIndex := startIndex + concurrency
		if endIndex > len(executions) {
			endIndex = len(executions)
		}
		batch := executions[startIndex:endIndex]
		start := time.Now().UTC()
		for _, execution := range batch {
			s.db.Model(&execution).Updates(map[string]any{"status": "running", "started_at": start})
		}
		timeout := time.Duration(job.TimeoutSeconds) * time.Second
		if s.mode == "ssh" {
			if s.runSSHBatch(batch, job.Script, timeout) {
				jobFailed = true
			}
			continue
		}
		if timeout > 0 && timeout < simulatedScriptExecutionDuration {
			time.Sleep(timeout)
			finish := time.Now().UTC()
			for _, execution := range batch {
				s.db.Model(&execution).Updates(map[string]any{"status": "failed", "exit_code": 124, "stderr": "simulated script timed out", "finished_at": finish})
			}
			jobFailed = true
			continue
		}
		time.Sleep(simulatedScriptExecutionDuration)
		finish := time.Now().UTC()
		for _, execution := range batch {
			out := fmt.Sprintf("Simulated script completed on server %d\nscript: %s", execution.ServerID, job.Script)
			s.db.Model(&execution).Updates(map[string]any{"status": "success", "exit_code": 0, "stdout": out, "finished_at": finish})
		}
	}
	finish := time.Now().UTC()
	status := "success"
	errorMessage := ""
	if jobFailed {
		status = "failed"
		errorMessage = "one or more script executions timed out"
	}
	s.db.Model(&job).Updates(map[string]any{"status": status, "error_message": errorMessage, "finished_at": finish})
}

func (s ScriptService) runSSHBatch(batch []models.ScriptExecution, script string, timeout time.Duration) bool {
	var wg sync.WaitGroup
	results := make(chan bool, len(batch))
	for _, execution := range batch {
		execution := execution
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			cancel := func() {}
			if timeout > 0 {
				ctxWithTimeout, cancelWithTimeout := context.WithTimeout(ctx, timeout)
				ctx = ctxWithTimeout
				cancel = cancelWithTimeout
			}
			defer cancel()
			result, err := s.executor.RunScriptForServer(ctx, execution.ServerID, script)
			finish := time.Now().UTC()
			status := "success"
			errorText := ""
			if err != nil || result.ExitCode != 0 {
				status = "failed"
				if err != nil {
					errorText = err.Error()
				}
				if ctx.Err() == context.DeadlineExceeded {
					result.ExitCode = 124
					errorText = "ssh script timed out"
				}
			}
			updates := map[string]any{"status": status, "exit_code": result.ExitCode, "stdout": result.Stdout, "stderr": result.Stderr, "finished_at": finish}
			if errorText != "" && strings.TrimSpace(result.Stderr) == "" {
				updates["stderr"] = errorText
			}
			s.db.Model(&execution).Updates(updates)
			results <- status == "failed"
		}()
	}
	wg.Wait()
	close(results)
	failed := false
	for itemFailed := range results {
		if itemFailed {
			failed = true
		}
	}
	return failed
}
