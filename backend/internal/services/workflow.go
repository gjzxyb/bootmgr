package services

import (
	"encoding/json"
	"errors"
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
const physicalPXEWorkflowName = "Physical PXE Installation"

var physicalPXEWorkflowSteps = []struct{ name, action string }{
	{"Manual power and PXE boot", "manual_power_pxe"},
	{"Physical PXE/iPXE boot event", "wait_physical_pxe"},
	{"Installer metadata access", "wait_metadata_access"},
	{"SSH or full evidence validation", "wait_ssh_or_full_evidence"},
}

func NewWorkflowService(db *gorm.DB, statusHistory StatusHistoryService, concurrency int) WorkflowService {
	if concurrency < 1 {
		concurrency = defaultDeploymentConcurrency
	}
	return WorkflowService{db: db, statusHistory: statusHistory, slots: make(chan struct{}, concurrency)}
}

func (s WorkflowService) StartDeployment(deploymentID uint) {
	go s.run(deploymentID)
}

func (s WorkflowService) ReconcileRunningDeployments() {
	var deployments []models.Deployment
	if err := s.db.Select("id", "status").Where("status IN ?", []string{"pending", "running"}).Find(&deployments).Error; err != nil {
		return
	}
	for _, deployment := range deployments {
		if deployment.Status == "pending" && !s.deploymentHasRunningWorkflow(deployment.ID) {
			s.StartDeployment(deployment.ID)
			continue
		}
		_ = s.ReconcileDeployment(deployment.ID)
	}
}

func (s WorkflowService) deploymentHasRunningWorkflow(deploymentID uint) bool {
	var workflows int64
	if err := s.db.Model(&models.WorkflowRun{}).Where("deployment_id = ? AND status = ?", deploymentID, "running").Count(&workflows).Error; err != nil {
		return true
	}
	return workflows > 0
}

func (s WorkflowService) ReconcileActiveDeploymentForServer(serverID uint) {
	var deployment models.Deployment
	if err := s.db.Where("server_id = ? AND status IN ?", serverID, []string{"pending", "running"}).Order("created_at desc").First(&deployment).Error; err != nil {
		return
	}
	_ = s.ReconcileDeployment(deployment.ID)
}

func (s WorkflowService) ReconcileDeployment(deploymentID uint) error {
	var dep models.Deployment
	if err := s.db.First(&dep, deploymentID).Error; err != nil {
		return err
	}
	if dep.Status != "pending" && dep.Status != "running" {
		return nil
	}
	var workflow models.WorkflowRun
	if err := s.db.Where("deployment_id = ? AND status = ?", deploymentID, "running").Order("id desc").First(&workflow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var tasks []models.TaskExecution
	if err := s.db.Where("workflow_run_id = ?", workflow.ID).Order("id asc").Find(&tasks).Error; err != nil {
		return err
	}
	if !workflowUsesPhysicalPXE(tasks) {
		return nil
	}

	evidence := s.deploymentPhysicalEvidence(dep)
	now := time.Now().UTC()
	taskByAction := map[string]models.TaskExecution{}
	for _, task := range tasks {
		taskByAction[task.Action] = task
	}

	if task, ok := taskByAction["manual_power_pxe"]; ok {
		if evidence.PXE != nil {
			s.updateTaskExecution(task, "success", fmt.Sprintf("Physical PXE boot observed for MAC %s in BootEvent #%d. Manual power/PXE action is complete.", evidence.PXE.MAC, evidence.PXE.ID), now)
		} else {
			s.updateTaskExecution(task, "running", strings.Join(manualPhysicalDeploymentActions(), "\n"), now)
		}
	}
	if task, ok := taskByAction["wait_physical_pxe"]; ok {
		if evidence.PXE != nil {
			s.updateTaskExecution(task, "success", fmt.Sprintf("Physical PXE/iPXE event #%d linked to deployment %d via %s from %s.", evidence.PXE.ID, dep.ID, evidence.PXE.Source, evidence.PXE.RemoteAddr), now)
		} else {
			s.updateTaskExecution(task, "pending", "Waiting for a physical PXE/iPXE boot event from the server primary MAC.", now)
		}
	}
	if task, ok := taskByAction["wait_metadata_access"]; ok {
		if evidence.Metadata {
			s.updateTaskExecution(task, "success", evidence.MetadataMessage, now)
		} else if evidence.PXE != nil {
			s.updateTaskExecution(task, "running", "PXE boot was observed; waiting for the installer to access deployment metadata.", now)
		} else {
			s.updateTaskExecution(task, "pending", "Waiting for PXE before metadata access can be proven.", now)
		}
	}
	if task, ok := taskByAction["wait_ssh_or_full_evidence"]; ok {
		if evidence.Final {
			s.updateTaskExecution(task, "success", evidence.FinalMessage, now)
		} else if evidence.Metadata {
			s.updateTaskExecution(task, "running", "Metadata was accessed; waiting for a successful SSH check or ok full/SSH lab evidence.", now)
		} else {
			s.updateTaskExecution(task, "pending", "Waiting for metadata access before post-install validation.", now)
		}
	}
	if evidence.PXE == nil || !evidence.Metadata || !evidence.Final {
		return nil
	}
	s.db.Model(&models.WorkflowRun{}).Where("id = ? AND status = ?", workflow.ID, "running").Updates(map[string]any{"status": "success", "finished_at": now})
	if result := s.db.Model(&models.Deployment{}).Where("id = ? AND status = ?", dep.ID, "running").Updates(map[string]any{"status": "success", "finished_at": now}); result.RowsAffected == 0 {
		return nil
	}
	var server models.Server
	if err := s.db.First(&server, dep.ServerID).Error; err == nil {
		beforeStatus := server.Status
		s.db.Model(&server).Updates(map[string]any{"status": "running", "deployed_at": now})
		_ = s.statusHistory.Record(server.ID, beforeStatus, "running", "deployment.success", "system")
	}
	return nil
}

func (s WorkflowService) run(deploymentID uint) {
	release := s.acquireSlot()
	defer release()

	now := time.Now().UTC()
	var dep models.Deployment
	if err := s.db.First(&dep, deploymentID).Error; err != nil {
		return
	}
	if dep.Status == "cancelled" {
		return
	}
	if s.deploymentRequiresPhysicalPXE(dep.ServerID) {
		s.startPhysicalPXEWorkflow(dep, now)
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

func (s WorkflowService) deploymentRequiresPhysicalPXE(serverID uint) bool {
	var endpoints int64
	if err := s.db.Model(&models.BmcEndpoint{}).Where("server_id = ?", serverID).Count(&endpoints).Error; err != nil {
		return false
	}
	return endpoints == 0
}

func (s WorkflowService) startPhysicalPXEWorkflow(dep models.Deployment, now time.Time) {
	workflow := models.WorkflowRun{DeploymentID: dep.ID, Name: physicalPXEWorkflowName, Version: "v1", Status: "running", Definition: "manual power -> physical PXE/iPXE -> installer metadata -> SSH/full evidence", StartedAt: &now}
	s.db.Create(&workflow)
	if result := s.db.Model(&models.Deployment{}).Where("id = ? AND status = ?", dep.ID, "pending").Updates(map[string]any{"status": "running", "started_at": now}); result.RowsAffected == 0 {
		s.finishCancelledWorkflow(workflow.ID)
		return
	}
	tasks := make([]models.TaskExecution, 0, len(physicalPXEWorkflowSteps))
	for i, step := range physicalPXEWorkflowSteps {
		task := models.TaskExecution{WorkflowRunID: workflow.ID, StepName: step.name, Action: step.action, Status: "pending"}
		if i == 0 {
			task.Status = "running"
			task.StartedAt = &now
			task.Stdout = strings.Join(manualPhysicalDeploymentActions(), "\n")
		}
		tasks = append(tasks, task)
	}
	s.db.Create(&tasks)
	_ = s.ReconcileDeployment(dep.ID)
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

type deploymentPhysicalEvidence struct {
	PXE             *models.BootEvent
	Metadata        bool
	MetadataMessage string
	Final           bool
	FinalMessage    string
}

func (s WorkflowService) deploymentPhysicalEvidence(dep models.Deployment) deploymentPhysicalEvidence {
	evidence := deploymentPhysicalEvidence{}
	startedAfter := dep.CreatedAt
	if dep.StartedAt != nil {
		startedAfter = *dep.StartedAt
	}
	var bootEvent models.BootEvent
	if err := s.db.Where("deployment_id = ? AND source IN ?", dep.ID, physicalBootEventSources()).Order("created_at desc, id desc").First(&bootEvent).Error; err == nil {
		evidence.PXE = &bootEvent
	}
	var token models.MetadataToken
	if err := s.db.Where("deployment_id = ? AND last_used_at IS NOT NULL", dep.ID).Order("last_used_at desc, id desc").First(&token).Error; err == nil {
		evidence.Metadata = true
		evidence.MetadataMessage = fmt.Sprintf("Deployment metadata token was used at %s.", token.LastUsedAt.Format(time.RFC3339))
	} else {
		var log models.LogEvent
		if err := s.db.Where("server_id = ? AND source = ? AND message LIKE ?", dep.ServerID, "metadata", fmt.Sprintf("%%deployment_id=%d%%", dep.ID)).Order("occurred_at desc, id desc").First(&log).Error; err == nil {
			evidence.Metadata = true
			evidence.MetadataMessage = fmt.Sprintf("Deployment metadata endpoint was accessed at %s.", log.OccurredAt.Format(time.RFC3339))
		}
	}
	var fullEvidence models.LabValidationEvidence
	if err := s.db.Where("server_id = ? AND status = ? AND kind IN ? AND created_at >= ?", dep.ServerID, "ok", []string{"full", "ssh"}, startedAfter).Order("created_at desc, id desc").First(&fullEvidence).Error; err == nil {
		evidence.Final = true
		evidence.FinalMessage = fmt.Sprintf("Lab validation %s evidence #%d recorded: %s.", fullEvidence.Kind, fullEvidence.ID, fullEvidence.Summary)
		return evidence
	}
	var sshAccess models.SSHAccess
	if err := s.db.Where("server_id = ? AND status = ? AND last_checked_at IS NOT NULL AND last_checked_at >= ?", dep.ServerID, "ok", startedAfter).Order("last_checked_at desc, id desc").First(&sshAccess).Error; err == nil {
		evidence.Final = true
		evidence.FinalMessage = fmt.Sprintf("SSH access check succeeded at %s for %s:%d.", sshAccess.LastCheckedAt.Format(time.RFC3339), sshAccess.Host, sshAccess.Port)
	}
	return evidence
}

func physicalBootEventSources() []string {
	return []string{"http_ipxe", "pxe_dhcp"}
}

func workflowUsesPhysicalPXE(tasks []models.TaskExecution) bool {
	for _, task := range tasks {
		if task.Action == "wait_physical_pxe" || task.Action == "manual_power_pxe" {
			return true
		}
	}
	return false
}

func manualPhysicalDeploymentActions() []string {
	return []string{
		"Manual power path: BMC is not configured for this target.",
		"Operator action: power on or reboot the physical server and choose PXE/Network Boot on the deployment NIC.",
		"Waiting for the platform to receive a physical PXE/iPXE boot event for the server MAC.",
	}
}

func (s WorkflowService) updateTaskExecution(task models.TaskExecution, status string, message string, now time.Time) {
	if task.Status == "success" || task.Status == "failed" || task.Status == "cancelled" {
		return
	}
	updates := map[string]any{"status": status, "stdout": message}
	switch status {
	case "running":
		if task.StartedAt == nil {
			updates["started_at"] = now
		}
	case "success":
		if task.StartedAt == nil {
			updates["started_at"] = now
		}
		updates["finished_at"] = now
	case "pending":
		if task.Status == "running" {
			return
		}
	}
	s.db.Model(&models.TaskExecution{}).Where("id = ?", task.ID).Updates(updates)
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
