package services

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"baremetal-platform/backend/internal/models"

	"gorm.io/gorm"
)

type WorkflowService struct {
	db            *gorm.DB
	statusHistory StatusHistoryService
	slots         chan struct{}
}

const defaultDeploymentConcurrency = 20

func NewWorkflowService(db *gorm.DB, statusHistory StatusHistoryService, concurrency int) WorkflowService {
	if concurrency < 1 {
		concurrency = defaultDeploymentConcurrency
	}
	return WorkflowService{db: db, statusHistory: statusHistory, slots: make(chan struct{}, concurrency)}
}

func (s WorkflowService) StartDeployment(deploymentID uint) {
	go s.run(deploymentID)
}

func (s WorkflowService) run(deploymentID uint) {
	release := s.acquireSlot()
	defer release()

	now := time.Now().UTC()
	var dep models.Deployment
	s.db.First(&dep, deploymentID)
	if dep.Status == "cancelled" {
		return
	}
	steps, workflowName, definition := s.stepsForDeployment(dep)
	workflow := models.WorkflowRun{DeploymentID: deploymentID, Name: workflowName, Version: "v1", Status: "running", Definition: definition, StartedAt: &now}
	s.db.Create(&workflow)
	if result := s.db.Model(&models.Deployment{}).Where("id = ? AND status = ?", deploymentID, "pending").Updates(map[string]any{"status": "running", "started_at": now}); result.RowsAffected == 0 {
		s.finishCancelledWorkflow(workflow.ID)
		return
	}

	for _, step := range steps {
		if s.deploymentCancelled(deploymentID) {
			s.finishCancelledWorkflow(workflow.ID)
			return
		}
		start := time.Now().UTC()
		task := models.TaskExecution{WorkflowRunID: workflow.ID, StepName: step.name, Action: step.action, Status: "running", StartedAt: &start, Stdout: fmt.Sprintf("%s started", step.name)}
		s.db.Create(&task)
		time.Sleep(350 * time.Millisecond)
		finish := time.Now().UTC()
		if s.deploymentCancelled(deploymentID) {
			s.db.Model(&task).Updates(map[string]any{"status": "cancelled", "finished_at": finish, "stdout": fmt.Sprintf("%s cancelled", step.name)})
			s.finishCancelledWorkflow(workflow.ID)
			return
		}
		if workflowStepShouldFail(step.action) {
			message := fmt.Sprintf("%s failed by MVP simulator", step.name)
			s.db.Model(&task).Updates(map[string]any{"status": "failed", "finished_at": finish, "stderr": message, "error_message": message})
			s.finishFailedWorkflow(deploymentID, workflow.ID, dep.ServerID, message, finish)
			return
		}
		s.db.Model(&task).Updates(map[string]any{"status": "success", "finished_at": finish, "stdout": fmt.Sprintf("%s completed by MVP simulator", step.name)})
	}
	finish := time.Now().UTC()
	s.db.Model(&models.WorkflowRun{}).Where("id = ?", workflow.ID).Updates(map[string]any{"status": "success", "finished_at": finish})
	if result := s.db.Model(&models.Deployment{}).Where("id = ? AND status = ?", deploymentID, "running").Updates(map[string]any{"status": "success", "finished_at": finish}); result.RowsAffected == 0 {
		return
	}

	if dep.ID != 0 {
		var server models.Server
		if err := s.db.First(&server, dep.ServerID).Error; err == nil {
			beforeStatus := server.Status
			s.db.Model(&server).Updates(map[string]any{"status": "running", "deployed_at": finish})
			_ = s.statusHistory.Record(server.ID, beforeStatus, "running", "deployment.success", "system")
		}
	}
}

func (s WorkflowService) acquireSlot() func() {
	if s.slots == nil {
		return func() {}
	}
	s.slots <- struct{}{}
	return func() { <-s.slots }
}

func (s WorkflowService) deploymentCancelled(deploymentID uint) bool {
	var dep models.Deployment
	if err := s.db.First(&dep, deploymentID).Error; err != nil {
		return true
	}
	return dep.Status == "cancelled"
}

func (s WorkflowService) finishCancelledWorkflow(workflowID uint) {
	finish := time.Now().UTC()
	s.db.Model(&models.WorkflowRun{}).Where("id = ? AND status = ?", workflowID, "running").Updates(map[string]any{"status": "cancelled", "finished_at": finish})
}

func (s WorkflowService) finishFailedWorkflow(deploymentID uint, workflowID uint, serverID uint, message string, finish time.Time) {
	s.db.Model(&models.WorkflowRun{}).Where("id = ? AND status = ?", workflowID, "running").Updates(map[string]any{"status": "failed", "finished_at": finish})
	if result := s.db.Model(&models.Deployment{}).Where("id = ? AND status = ?", deploymentID, "running").Updates(map[string]any{"status": "failed", "finished_at": finish, "error_message": message}); result.RowsAffected == 0 {
		return
	}
	var server models.Server
	if err := s.db.First(&server, serverID).Error; err == nil && server.Status == "deploying" {
		s.db.Model(&server).Update("status", "ready")
		_ = s.statusHistory.Record(server.ID, "deploying", "ready", "deployment.failed", "system")
	}
}

func workflowStepShouldFail(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "fail", "simulate_fail", "simulate_failure":
		return true
	default:
		return false
	}
}

type workflowStep struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

func (s WorkflowService) stepsForDeployment(dep models.Deployment) ([]struct{ name, action string }, string, string) {
	defaults := []struct{ name, action string }{{"Hardware discovery", "discover_hardware"}, {"Prepare iPXE script", "render_ipxe"}, {"Simulated OS install", "install_os"}, {"Post install callback", "register_host"}}
	if dep.WorkflowID == nil {
		return defaults, "MVP Linux Installation", "discover -> prepare -> install -> callback"
	}
	var tmpl models.WorkflowTemplate
	if err := s.db.First(&tmpl, *dep.WorkflowID).Error; err != nil {
		return defaults, "MVP Linux Installation", "discover -> prepare -> install -> callback"
	}
	var parsed struct {
		Steps []workflowStep `json:"steps"`
	}
	if err := json.Unmarshal(tmpl.Definition, &parsed); err != nil || len(parsed.Steps) == 0 {
		return defaults, tmpl.Name, string(tmpl.Definition)
	}
	steps := make([]struct{ name, action string }, 0, len(parsed.Steps))
	for _, step := range parsed.Steps {
		steps = append(steps, struct{ name, action string }{step.Name, step.Action})
	}
	return steps, tmpl.Name, string(tmpl.Definition)
}
