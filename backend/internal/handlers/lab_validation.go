package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"baremetal-platform/backend/internal/config"
	"baremetal-platform/backend/internal/middleware"
	"baremetal-platform/backend/internal/models"
	"baremetal-platform/backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

const labEvidenceFreshnessWindow = 7 * 24 * time.Hour

type labValidationCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type labValidationEnvironment struct {
	AppEnv            string `json:"app_env"`
	BootBaseURL       string `json:"boot_base_url"`
	BMCAdapter        string `json:"bmc_adapter"`
	CollectorMode     string `json:"collector_mode"`
	SSHOperationsMode string `json:"ssh_operations_mode"`
}

type labValidationPXE struct {
	Enabled             bool           `json:"enabled"`
	Mode                string         `json:"mode"`
	BindInterface       string         `json:"bind_interface"`
	DHCPListenAddr      string         `json:"dhcp_listen_addr"`
	ProxyDHCPListenAddr string         `json:"proxy_dhcp_listen_addr,omitempty"`
	DHCPServerIP        string         `json:"dhcp_server_ip"`
	DHCPLeaseStart      string         `json:"dhcp_lease_start"`
	DHCPLeaseEnd        string         `json:"dhcp_lease_end"`
	TFTPListenAddr      string         `json:"tftp_listen_addr"`
	TFTPRoot            string         `json:"tftp_root"`
	BootfileUEFI        string         `json:"bootfile_uefi"`
	BootfileBIOS        string         `json:"bootfile_bios"`
	DeploymentNetworks  int64          `json:"deployment_networks"`
	BootEvents          int64          `json:"boot_events"`
	RecentBootEvents    []labBootEvent `json:"recent_boot_events"`
	RuntimeIssues       []config.Issue `json:"runtime_issues"`
}

type labBootEvent struct {
	ID           uint      `json:"id"`
	MAC          string    `json:"mac"`
	Architecture string    `json:"architecture"`
	Firmware     string    `json:"firmware"`
	RemoteAddr   string    `json:"remote_addr"`
	Source       string    `json:"source"`
	ServerID     *uint     `json:"server_id,omitempty"`
	DeploymentID *uint     `json:"deployment_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type labValidationBMC struct {
	Adapter         string      `json:"adapter"`
	Total           int64       `json:"total"`
	OK              int64       `json:"ok"`
	Error           int64       `json:"error"`
	Unknown         int64       `json:"unknown"`
	LastCheckedAt   *time.Time  `json:"last_checked_at,omitempty"`
	RecentEndpoints []labBMCRef `json:"recent_endpoints"`
}

type labBMCRef struct {
	ServerID      uint       `json:"server_id"`
	Hostname      string     `json:"hostname"`
	AssetNo       string     `json:"asset_no"`
	Type          string     `json:"type"`
	Protocol      string     `json:"protocol"`
	Endpoint      string     `json:"endpoint"`
	Status        string     `json:"status"`
	PowerState    string     `json:"power_state"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type labValidationSSH struct {
	CollectorMode     string      `json:"collector_mode"`
	OperationsMode    string      `json:"operations_mode"`
	Total             int64       `json:"total"`
	OK                int64       `json:"ok"`
	Error             int64       `json:"error"`
	Configured        int64       `json:"configured"`
	Unknown           int64       `json:"unknown"`
	LastCheckedAt     *time.Time  `json:"last_checked_at,omitempty"`
	RecentSSHAccesses []labSSHRef `json:"recent_ssh_accesses"`
}

type labSSHRef struct {
	ServerID      uint       `json:"server_id"`
	Hostname      string     `json:"hostname"`
	AssetNo       string     `json:"asset_no"`
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	Username      string     `json:"username"`
	AuthType      string     `json:"auth_type"`
	Status        string     `json:"status"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type labTerminalSessionRef struct {
	ID          uint       `json:"id"`
	ServerID    uint       `json:"server_id"`
	Hostname    string     `json:"hostname"`
	AssetNo     string     `json:"asset_no"`
	Status      string     `json:"status"`
	Mode        string     `json:"mode"`
	RequestedBy string     `json:"requested_by"`
	Reason      string     `json:"reason"`
	Transcript  string     `json:"transcript"`
	OpenedAt    time.Time  `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type labScriptExecutionRef struct {
	ID          uint       `json:"id"`
	ScriptJobID uint       `json:"script_job_id"`
	ServerID    uint       `json:"server_id"`
	Hostname    string     `json:"hostname"`
	AssetNo     string     `json:"asset_no"`
	JobName     string     `json:"job_name"`
	Status      string     `json:"status"`
	ExitCode    int        `json:"exit_code"`
	Stdout      string     `json:"stdout"`
	Stderr      string     `json:"stderr"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type labLogEventRef struct {
	ID         uint      `json:"id"`
	ServerID   uint      `json:"server_id"`
	Hostname   string    `json:"hostname"`
	AssetNo    string    `json:"asset_no"`
	Source     string    `json:"source"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	TraceID    string    `json:"trace_id"`
	OccurredAt time.Time `json:"occurred_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type labValidationRunResult struct {
	ID        uint           `json:"id,omitempty"`
	RunID     uint           `json:"run_id,omitempty"`
	Kind      string         `json:"kind"`
	ServerID  uint           `json:"server_id"`
	Hostname  string         `json:"hostname"`
	AssetNo   string         `json:"asset_no"`
	Status    string         `json:"status"`
	Message   string         `json:"message"`
	Details   datatypes.JSON `json:"details,omitempty"`
	CheckedAt *time.Time     `json:"checked_at,omitempty"`
}

type labValidationRunSummary struct {
	ID              uint       `json:"id"`
	Status          string     `json:"status"`
	Strict          bool       `json:"strict"`
	CheckPXE        bool       `json:"check_pxe"`
	CheckBMC        bool       `json:"check_bmc"`
	CheckSSH        bool       `json:"check_ssh"`
	Limit           int        `json:"limit"`
	ServerIDs       []uint     `json:"server_ids"`
	PXEMACs         []string   `json:"pxe_macs"`
	PXEProbeMAC     string     `json:"pxe_probe_mac,omitempty"`
	PXEArch         uint16     `json:"pxe_arch"`
	SSHProbeCommand string     `json:"ssh_probe_command,omitempty"`
	RequestedBy     string     `json:"requested_by"`
	RequestID       string     `json:"request_id"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Results         int64      `json:"results"`
	Failures        int64      `json:"failures"`
	Skipped         int64      `json:"skipped"`
}

type labValidationRunDetail struct {
	Run     labValidationRunSummary  `json:"run"`
	Results []labValidationRunResult `json:"results"`
}

type labValidationEvidenceBundle struct {
	GeneratedAt        time.Time                  `json:"generated_at"`
	Run                labValidationRunSummary    `json:"run"`
	Environment        labValidationEnvironment   `json:"environment"`
	Checks             []labValidationCheck       `json:"checks"`
	Results            []labValidationRunResult   `json:"results"`
	BootEvents         []labBootEvent             `json:"boot_events"`
	BMCEndpoints       []labBMCRef                `json:"bmc_endpoints"`
	SSHAccesses        []labSSHRef                `json:"ssh_accesses"`
	TerminalSessions   []labTerminalSessionRef    `json:"terminal_sessions"`
	ScriptExecutions   []labScriptExecutionRef    `json:"script_executions"`
	LogEvents          []labLogEventRef           `json:"log_events"`
	RecentEvidence     []labValidationEvidence    `json:"recent_evidence"`
	Targets            []labValidationTarget      `json:"targets"`
	ConfigIssues       []config.Issue             `json:"config_issues"`
	RuntimePXEIssues   []config.Issue             `json:"pxe_runtime_issues"`
	Notes              []string                   `json:"notes"`
	OperatorChecklist  []labOperatorChecklistItem `json:"operator_checklist"`
	EvidenceCandidates []labEvidenceCandidate     `json:"evidence_candidates"`
}

type labOperatorChecklistItem struct {
	Subject         string   `json:"subject"`
	Step            string   `json:"step"`
	Status          string   `json:"status"`
	Message         string   `json:"message"`
	NextAction      string   `json:"next_action,omitempty"`
	RunID           uint     `json:"run_id"`
	ServerID        uint     `json:"server_id,omitempty"`
	BootEventID     *uint    `json:"boot_event_id,omitempty"`
	EvidenceID      *uint    `json:"evidence_id,omitempty"`
	BlockingReasons []string `json:"blocking_reasons,omitempty"`
}

type labEvidenceCandidate struct {
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Subject     string `json:"subject"`
	Summary     string `json:"summary"`
	Details     string `json:"details,omitempty"`
	RunID       uint   `json:"run_id"`
	ServerID    uint   `json:"server_id,omitempty"`
	BootEventID uint   `json:"boot_event_id,omitempty"`
	SourceStep  string `json:"source_step"`
}

type labValidationRunOptions struct {
	CheckBMC        *bool    `json:"check_bmc"`
	CheckSSH        *bool    `json:"check_ssh"`
	CheckPXE        *bool    `json:"check_pxe"`
	Strict          bool     `json:"strict"`
	Limit           int      `json:"limit"`
	ServerIDs       []uint   `json:"server_ids"`
	PXEMACs         []string `json:"pxe_macs"`
	PXEProbeMAC     string   `json:"pxe_probe_mac"`
	PXEArch         *uint16  `json:"pxe_arch"`
	SSHProbeCommand string   `json:"ssh_probe_command"`
	pxeArch         uint16
}

type labValidationEvidence struct {
	ID            uint      `json:"id"`
	Kind          string    `json:"kind"`
	Subject       string    `json:"subject"`
	Status        string    `json:"status"`
	Summary       string    `json:"summary"`
	Details       string    `json:"details,omitempty"`
	ArtifactURL   string    `json:"artifact_url,omitempty"`
	RunID         *uint     `json:"run_id,omitempty"`
	ServerID      *uint     `json:"server_id,omitempty"`
	BootEventID   *uint     `json:"boot_event_id,omitempty"`
	BmcEndpointID *uint     `json:"bmc_endpoint_id,omitempty"`
	SSHAccessID   *uint     `json:"ssh_access_id,omitempty"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

type labValidationTarget struct {
	ServerID              uint       `json:"server_id"`
	Hostname              string     `json:"hostname"`
	AssetNo               string     `json:"asset_no"`
	PrimaryMAC            string     `json:"primary_mac"`
	ServerStatus          string     `json:"server_status"`
	PXEStatus             string     `json:"pxe_status"`
	PXEBootEventID        *uint      `json:"pxe_boot_event_id,omitempty"`
	PXEBootAt             *time.Time `json:"pxe_boot_at,omitempty"`
	BMCRequired           bool       `json:"bmc_required"`
	BMCStatus             string     `json:"bmc_status"`
	BMCCheckedAt          *time.Time `json:"bmc_checked_at,omitempty"`
	SSHStatus             string     `json:"ssh_status"`
	SSHCheckedAt          *time.Time `json:"ssh_checked_at,omitempty"`
	EvidenceStatus        string     `json:"evidence_status"`
	EvidenceID            *uint      `json:"evidence_id,omitempty"`
	EvidenceKinds         []string   `json:"evidence_kinds"`
	LatestRunID           *uint      `json:"latest_run_id,omitempty"`
	LatestRunStatus       string     `json:"latest_run_status,omitempty"`
	LatestRunStrict       bool       `json:"latest_run_strict,omitempty"`
	LatestRunKind         string     `json:"latest_run_kind,omitempty"`
	LatestRunResultStatus string     `json:"latest_run_result_status,omitempty"`
	LatestRunAt           *time.Time `json:"latest_run_at,omitempty"`
	FullChainReady        bool       `json:"full_chain_ready"`
	BlockingReasons       []string   `json:"blocking_reasons"`
}

type labValidationReport struct {
	Status         string                    `json:"status"`
	RunID          uint                      `json:"run_id,omitempty"`
	GeneratedAt    time.Time                 `json:"generated_at"`
	Environment    labValidationEnvironment  `json:"environment"`
	Checks         []labValidationCheck      `json:"checks"`
	PXE            labValidationPXE          `json:"pxe"`
	BMC            labValidationBMC          `json:"bmc"`
	SSH            labValidationSSH          `json:"ssh"`
	RecentEvidence []labValidationEvidence   `json:"recent_evidence"`
	Targets        []labValidationTarget     `json:"targets"`
	RecentRuns     []labValidationRunSummary `json:"recent_runs"`
	RunResults     []labValidationRunResult  `json:"run_results,omitempty"`
}

func (h Handler) getLabValidation(c *gin.Context) {
	c.JSON(http.StatusOK, h.buildLabValidationReport(nil, 0))
}

func (h Handler) getLabValidationRun(c *gin.Context) {
	detail, ok := h.labValidationRunDetailFromParam(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h Handler) getLabValidationRunEvidenceBundle(c *gin.Context) {
	detail, ok := h.labValidationRunDetailFromParam(c)
	if !ok {
		return
	}
	bundle := h.buildLabValidationEvidenceBundle(detail)
	actorID, actorEmail := middleware.Actor(c)
	h.audit.Record(actorID, actorEmail, "system.lab_validation.evidence_bundle", "lab_validation_run", detail.Run.ID, "low", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, bundle)
}

func (h Handler) labValidationRunDetailFromParam(c *gin.Context) (labValidationRunDetail, bool) {
	id := uintFromParam(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid lab validation run id"})
		return labValidationRunDetail{}, false
	}
	detail, err := h.labValidationRunDetail(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "lab validation run not found"})
		return labValidationRunDetail{}, false
	}
	return detail, true
}

func (h Handler) labValidationRunDetail(id uint) (labValidationRunDetail, error) {
	var row models.LabValidationRun
	if err := h.db.First(&row, id).Error; err != nil {
		return labValidationRunDetail{}, err
	}
	summary := labValidationRunFromModel(row)
	h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ?", row.ID).Count(&summary.Results)
	h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ? AND status = ?", row.ID, "failed").Count(&summary.Failures)
	h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ? AND status = ?", row.ID, "skipped").Count(&summary.Skipped)
	var resultRows []models.LabValidationRunResult
	h.db.Where("run_id = ?", row.ID).Order("id asc").Find(&resultRows)
	results := make([]labValidationRunResult, 0, len(resultRows))
	for _, result := range resultRows {
		results = append(results, labValidationRunResultFromModel(result))
	}
	return labValidationRunDetail{Run: summary, Results: results}, nil
}

func (h Handler) buildLabValidationEvidenceBundle(detail labValidationRunDetail) labValidationEvidenceBundle {
	report := h.buildLabValidationReport(detail.Results, detail.Run.ID)
	serverIDs := labRunServerIDSet(detail)
	bootEvents := h.labBootEventsForBundle(detail)
	bmcEndpoints := h.labBMCEndpointsForBundle(serverIDs)
	sshAccesses := h.labSSHAccessesForBundle(serverIDs)
	terminalSessions := h.labTerminalSessionsForBundle(serverIDs)
	scriptExecutions := h.labScriptExecutionsForBundle(serverIDs)
	logEvents := h.labLogEventsForBundle(serverIDs)
	recentEvidence := h.labEvidenceForBundle(detail, bootEvents, serverIDs)
	validation := config.Validate(h.cfg)
	notes := []string{}
	if detail.Run.Strict {
		notes = append(notes, "strict mode was enabled for this run")
	}
	if detail.Run.Status != "ok" {
		notes = append(notes, "run status is not ok; inspect failed and skipped results before accepting physical validation")
	}
	if h.bmc.AdapterName() == "simulated" && len(bmcEndpoints) > 0 {
		notes = append(notes, "BMC_ADAPTER=simulated cannot prove physical Redfish/IPMI validation for targets with BMC endpoints")
	}
	if len(bootEvents) == 0 && detail.Run.CheckPXE {
		notes = append(notes, "no referenced PXE boot event is included in this bundle")
	}
	operatorChecklist := h.labOperatorChecklist(detail, report.Targets, bootEvents)
	return labValidationEvidenceBundle{
		GeneratedAt:        time.Now().UTC(),
		Run:                detail.Run,
		Environment:        report.Environment,
		Checks:             report.Checks,
		Results:            detail.Results,
		BootEvents:         bootEvents,
		BMCEndpoints:       bmcEndpoints,
		SSHAccesses:        sshAccesses,
		TerminalSessions:   terminalSessions,
		ScriptExecutions:   scriptExecutions,
		LogEvents:          logEvents,
		RecentEvidence:     recentEvidence,
		Targets:            report.Targets,
		ConfigIssues:       append(validation.Errors, validation.Warnings...),
		RuntimePXEIssues:   config.BootRuntimeIssues(h.cfg),
		Notes:              notes,
		OperatorChecklist:  operatorChecklist,
		EvidenceCandidates: h.labEvidenceCandidates(detail.Run.ID, operatorChecklist, report.Targets, bootEvents),
	}
}

func (h Handler) labEvidenceCandidates(runID uint, checklist []labOperatorChecklistItem, targets []labValidationTarget, bootEvents []labBootEvent) []labEvidenceCandidate {
	targetByServer := map[uint]labValidationTarget{}
	for _, target := range targets {
		if target.ServerID != 0 {
			targetByServer[target.ServerID] = target
		}
	}
	bootEventByID := map[uint]labBootEvent{}
	for _, event := range bootEvents {
		if event.ID != 0 {
			bootEventByID[event.ID] = event
		}
	}
	stepOK := func(serverID uint, step string) bool {
		if serverID == 0 {
			return false
		}
		for _, item := range checklist {
			if item.ServerID == serverID && item.Step == step && item.Status == "ok" {
				return true
			}
		}
		return false
	}
	bmcStepSatisfied := func(serverID uint) bool {
		if target, ok := targetByServer[serverID]; ok && !target.BMCRequired {
			return true
		}
		return stepOK(serverID, "bmc_identity")
	}
	subjectForServer := func(serverID uint, fallback string) string {
		if target, ok := targetByServer[serverID]; ok {
			if subject := labTargetSubject(target); subject != "" {
				return subject
			}
		}
		if strings.TrimSpace(fallback) != "" {
			return fallback
		}
		if serverID != 0 {
			return fmt.Sprintf("server:%d", serverID)
		}
		return "unknown"
	}
	candidates := []labEvidenceCandidate{}
	seen := map[string]bool{}
	add := func(candidate labEvidenceCandidate) {
		if candidate.RunID == 0 {
			candidate.RunID = runID
		}
		if candidate.Status == "" {
			candidate.Status = "ok"
		}
		key := fmt.Sprintf("%s:%d:%d:%s", candidate.Kind, candidate.ServerID, candidate.BootEventID, candidate.Subject)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}
	for _, item := range checklist {
		switch item.Step {
		case "pxe_boot_event":
			if item.Status != "ok" || item.BootEventID == nil || *item.BootEventID == 0 {
				continue
			}
			event, ok := bootEventByID[*item.BootEventID]
			if !ok || !labBootEventCountsAsPXEProof(event.Source) || strings.TrimSpace(event.MAC) == "" {
				continue
			}
			candidate := labEvidenceCandidate{
				Kind: "pxe", Status: "ok", Subject: event.MAC, RunID: runID, BootEventID: event.ID,
				SourceStep: "pxe_boot_event",
				Summary:    "Physical PXE boot event recorded by lab validation",
				Details:    fmt.Sprintf("Strict lab validation run %d observed physical PXE BootEvent #%d for MAC %s via %s.", runID, event.ID, event.MAC, bootEventSourceLabel(event.Source)),
			}
			if item.ServerID != 0 {
				candidate.ServerID = item.ServerID
				candidate.Details += fmt.Sprintf(" Linked server_id %d.", item.ServerID)
			}
			add(candidate)
		case "bmc_identity":
			if item.Status != "ok" || item.ServerID == 0 {
				continue
			}
			subject := subjectForServer(item.ServerID, item.Subject)
			add(labEvidenceCandidate{
				Kind: "bmc", Status: "ok", Subject: subject, RunID: runID, ServerID: item.ServerID,
				SourceStep: "bmc_identity",
				Summary:    "Physical Redfish/IPMI identity evidence recorded by lab validation",
				Details:    fmt.Sprintf("Strict lab validation run %d produced physical Redfish/IPMI identity proof for server_id %d.", runID, item.ServerID),
			})
		case "ssh_command":
			if item.Status != "ok" || item.ServerID == 0 {
				continue
			}
			subject := subjectForServer(item.ServerID, item.Subject)
			add(labEvidenceCandidate{
				Kind: "ssh", Status: "ok", Subject: subject, RunID: runID, ServerID: item.ServerID,
				SourceStep: "ssh_command",
				Summary:    "Real SSH known_hosts command evidence recorded by lab validation",
				Details:    fmt.Sprintf("Strict lab validation run %d produced real SSH known_hosts host key proof and command proof for server_id %d.", runID, item.ServerID),
			})
		case "full_chain_evidence":
			if item.ServerID == 0 || item.BootEventID == nil || *item.BootEventID == 0 || item.EvidenceID != nil {
				continue
			}
			if !stepOK(item.ServerID, "pxe_boot_event") || !bmcStepSatisfied(item.ServerID) || !stepOK(item.ServerID, "ssh_command") {
				continue
			}
			subject := subjectForServer(item.ServerID, item.Subject)
			proofSummary := "physical PXE BootEvent and SSH command proof"
			proofDetails := fmt.Sprintf("Strict lab validation run %d produced physical PXE BootEvent #%d and SSH command proof for server_id %d. BMC was not configured for this target and was treated as optional.", runID, *item.BootEventID, item.ServerID)
			if target, ok := targetByServer[item.ServerID]; !ok || target.BMCRequired {
				proofSummary = "physical PXE BootEvent, BMC identity proof, and SSH command proof"
				proofDetails = fmt.Sprintf("Strict lab validation run %d produced physical PXE BootEvent #%d, BMC identity proof, and SSH command proof for server_id %d.", runID, *item.BootEventID, item.ServerID)
			}
			add(labEvidenceCandidate{
				Kind: "full", Status: "ok", Subject: subject, RunID: runID, ServerID: item.ServerID, BootEventID: *item.BootEventID,
				SourceStep: "full_chain_evidence",
				Summary:    "Full-chain " + proofSummary + " recorded by lab validation",
				Details:    proofDetails,
			})
		}
	}
	return candidates
}

func labTargetSubject(target labValidationTarget) string {
	if strings.TrimSpace(target.AssetNo) != "" {
		return strings.TrimSpace(target.AssetNo)
	}
	if strings.TrimSpace(target.Hostname) != "" {
		return strings.TrimSpace(target.Hostname)
	}
	if target.ServerID != 0 {
		return fmt.Sprintf("server:%d", target.ServerID)
	}
	return ""
}

func (h Handler) labOperatorChecklist(detail labValidationRunDetail, targets []labValidationTarget, bootEvents []labBootEvent) []labOperatorChecklistItem {
	items := []labOperatorChecklistItem{}
	runID := detail.Run.ID
	targetServerIDs := labRunServerIDSet(detail)
	bmcResults := labRunResultsByServer(detail.Results, "bmc")
	sshResults := labRunResultsByServer(detail.Results, "ssh")
	bootEventByMAC := map[string]labBootEvent{}
	for _, event := range bootEvents {
		if mac := normalizeLabMAC(event.MAC); mac != "" {
			bootEventByMAC[mac] = event
		}
	}
	coveredPXEMACs := map[string]bool{}
	add := func(item labOperatorChecklistItem) {
		if item.RunID == 0 {
			item.RunID = runID
		}
		items = append(items, item)
	}
	for _, target := range targets {
		if len(targetServerIDs) > 0 && !targetServerIDs[target.ServerID] {
			continue
		}
		subject := target.AssetNo
		if subject == "" {
			subject = target.Hostname
		}
		if subject == "" {
			subject = fmt.Sprintf("server:%d", target.ServerID)
		}
		if mac := normalizeLabMAC(target.PrimaryMAC); mac != "" {
			coveredPXEMACs[mac] = true
		}
		pxeStatus := "pending"
		pxeMessage := "waiting for physical PXE client boot event from http_ipxe or pxe_dhcp"
		pxeNextAction := "boot the physical server from the isolated PXE network, then rerun strict lab validation with this server_id and pxe_macs"
		if target.PXEStatus == "ok" && target.PXEBootEventID != nil {
			pxeStatus = "ok"
			pxeMessage = fmt.Sprintf("physical PXE boot event #%d is linked to this target", *target.PXEBootEventID)
			pxeNextAction = ""
		} else if target.PXEStatus == "error" {
			pxeStatus = "error"
			pxeMessage = "latest PXE evidence for this target cannot prove a physical boot"
		}
		add(labOperatorChecklistItem{
			Subject: subject, Step: "pxe_boot_event", Status: pxeStatus, Message: pxeMessage, NextAction: pxeNextAction,
			ServerID: target.ServerID, BootEventID: target.PXEBootEventID, BlockingReasons: target.BlockingReasons,
		})

		bmcStatus := "pending"
		bmcMessage := "waiting for physical Redfish/IPMI identity probe"
		bmcNextAction := "configure a matching BMC endpoint, run strict lab validation, and verify BMC result details contain non-secret identity fields"
		if !target.BMCRequired {
			bmcStatus = "skipped"
			bmcMessage = "BMC is not configured for this target and is optional"
			bmcNextAction = ""
		} else if result, ok := bmcResults[target.ServerID]; ok && labBMCResultHasIdentityProof(result) {
			bmcStatus = "ok"
			bmcMessage = "this run includes successful physical BMC identity proof"
			bmcNextAction = ""
		} else if result, ok := bmcResults[target.ServerID]; ok && result.Status == "success" {
			bmcStatus = "warning"
			bmcMessage = "BMC check succeeded, but this run lacks structured identity proof details"
		} else if result, ok := bmcResults[target.ServerID]; ok && result.Status == "failed" {
			bmcStatus = "error"
			bmcMessage = "BMC check failed in this run: " + result.Message
		} else if target.BMCStatus == "ok" {
			bmcStatus = "warning"
			bmcMessage = "BMC is currently fresh ok, but this bundle has no successful BMC identity result for this run"
		} else if target.BMCStatus == "error" {
			bmcStatus = "error"
			bmcMessage = "BMC check is failing for this target"
		}
		add(labOperatorChecklistItem{
			Subject: subject, Step: "bmc_identity", Status: bmcStatus, Message: bmcMessage, NextAction: bmcNextAction,
			ServerID: target.ServerID, BlockingReasons: target.BlockingReasons,
		})

		sshStatus := "pending"
		sshMessage := "waiting for real SSH command proof"
		sshNextAction := "configure SSH access with SSH_HOST_KEY_POLICY=known_hosts, run strict lab validation, and verify SSH result details include host key proof, command, exit_code, and stdout"
		if result, ok := sshResults[target.ServerID]; ok && labSSHResultHasCommandProof(result) {
			sshStatus = "ok"
			sshMessage = "this run includes successful real SSH known_hosts and command proof"
			sshNextAction = ""
		} else if result, ok := sshResults[target.ServerID]; ok && result.Status == "success" {
			sshStatus = "warning"
			sshMessage = "SSH check succeeded, but this run lacks structured known_hosts or command proof details"
		} else if result, ok := sshResults[target.ServerID]; ok && result.Status == "failed" {
			sshStatus = "error"
			sshMessage = "SSH check failed in this run: " + result.Message
		} else if target.SSHStatus == "ok" {
			sshStatus = "warning"
			sshMessage = "SSH is currently fresh ok, but this bundle has no successful SSH command result for this run"
		} else if target.SSHStatus == "error" {
			sshStatus = "error"
			sshMessage = "SSH check is failing for this target"
		}
		add(labOperatorChecklistItem{
			Subject: subject, Step: "ssh_command", Status: sshStatus, Message: sshMessage, NextAction: sshNextAction,
			ServerID: target.ServerID, BlockingReasons: target.BlockingReasons,
		})

		fullStatus := "pending"
		fullMessage := "waiting for full-chain physical evidence"
		fullNextAction := "record ok full evidence with this run_id, server_id, and boot_event_id after PXE and SSH target results are successful"
		if target.BMCRequired {
			fullNextAction = "record ok full evidence with this run_id, server_id, and boot_event_id after PXE, BMC, and SSH target results are successful"
		}
		if target.FullChainReady {
			fullStatus = "ok"
			fullMessage = "full-chain physical validation is ready for this target"
			fullNextAction = ""
		} else if len(target.BlockingReasons) > 0 {
			fullStatus = "warning"
			fullMessage = "full-chain physical validation is not ready for this target"
		}
		add(labOperatorChecklistItem{
			Subject: subject, Step: "full_chain_evidence", Status: fullStatus, Message: fullMessage, NextAction: fullNextAction,
			ServerID: target.ServerID, BootEventID: target.PXEBootEventID, EvidenceID: target.EvidenceID, BlockingReasons: target.BlockingReasons,
		})
	}
	for _, mac := range detail.Run.PXEMACs {
		normalized := normalizeLabMAC(mac)
		if normalized == "" {
			continue
		}
		if coveredPXEMACs[normalized] {
			continue
		}
		status := "pending"
		message := "waiting for physical PXE client boot event from http_ipxe or pxe_dhcp"
		var bootEventID *uint
		if event, ok := bootEventByMAC[normalized]; ok && labBootEventCountsAsPXEProof(event.Source) {
			status = "ok"
			message = fmt.Sprintf("physical PXE boot event #%d is recorded for this MAC", event.ID)
			bootEventID = &event.ID
		}
		add(labOperatorChecklistItem{
			Subject: normalized, Step: "pxe_boot_event", Status: status, Message: message,
			NextAction: "boot the physical PXE client and rerun strict lab validation with this pxe_macs value", BootEventID: bootEventID,
		})
	}
	return items
}

func labRunResultsByServer(results []labValidationRunResult, kind string) map[uint]labValidationRunResult {
	byServer := map[uint]labValidationRunResult{}
	for _, result := range results {
		if result.Kind != kind || result.ServerID == 0 {
			continue
		}
		if current, ok := byServer[result.ServerID]; !ok || current.Status != "success" {
			byServer[result.ServerID] = result
		}
	}
	return byServer
}

func labBMCResultHasIdentityProof(result labValidationRunResult) bool {
	if result.Status != "success" || len(result.Details) == 0 {
		return false
	}
	var details map[string]any
	if err := json.Unmarshal(result.Details, &details); err != nil {
		return false
	}
	adapter, ok := details["adapter"].(string)
	if !ok || !stringIn(strings.ToLower(strings.TrimSpace(adapter)), "redfish", "ipmi") {
		return false
	}
	for _, key := range []string{"manufacturer", "manufacturer_id", "model", "product_id", "device_id", "device_revision", "serial_number", "firmware_version", "bios_version", "bmc_version"} {
		if value, ok := details[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func labSSHResultHasCommandProof(result labValidationRunResult) bool {
	if result.Status != "success" || len(result.Details) == 0 {
		return false
	}
	var details map[string]any
	if err := json.Unmarshal(result.Details, &details); err != nil {
		return false
	}
	command, ok := details["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return false
	}
	stdout, ok := details["stdout"].(string)
	if !ok || strings.TrimSpace(stdout) == "" {
		return false
	}
	policy, ok := details["host_key_policy"].(string)
	if !ok || strings.ToLower(strings.TrimSpace(policy)) != "known_hosts" {
		return false
	}
	verified, ok := details["host_key_verified"].(bool)
	if !ok || !verified {
		return false
	}
	algorithm, ok := details["host_key_algorithm"].(string)
	if !ok || strings.TrimSpace(algorithm) == "" {
		return false
	}
	fingerprint, ok := details["host_key_sha256"].(string)
	if !ok || !strings.HasPrefix(strings.TrimSpace(fingerprint), "SHA256:") {
		return false
	}
	if _, ok := details["error"]; ok {
		return false
	}
	switch code := details["exit_code"].(type) {
	case float64:
		return code == 0
	case int:
		return code == 0
	default:
		return false
	}
}

func (h Handler) recordLabValidationEvidence(c *gin.Context) {
	var req struct {
		Kind        string `json:"kind"`
		Subject     string `json:"subject"`
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Details     string `json:"details"`
		ArtifactURL string `json:"artifact_url"`
		RunID       *uint  `json:"run_id"`
		ServerID    *uint  `json:"server_id"`
		BootEventID *uint  `json:"boot_event_id"`
	}
	if !bind(c, &req) {
		return
	}
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.Subject = strings.TrimSpace(req.Subject)
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	req.Summary = strings.TrimSpace(req.Summary)
	req.Details = strings.TrimSpace(req.Details)
	req.ArtifactURL = strings.TrimSpace(req.ArtifactURL)
	if req.Status == "" {
		req.Status = "ok"
	}
	if !stringIn(req.Kind, "pxe", "bmc", "ssh", "full") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be one of pxe, bmc, ssh, full"})
		return
	}
	if !stringIn(req.Status, "ok", "warning", "error") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be one of ok, warning, error"})
		return
	}
	if req.Subject == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "subject is required"})
		return
	}
	if req.Summary == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "summary is required"})
		return
	}
	if len([]rune(req.Subject)) > 160 || len([]rune(req.Summary)) > 500 || len([]rune(req.Details)) > 4000 || len([]rune(req.ArtifactURL)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "evidence fields exceed length limits"})
		return
	}
	if req.ArtifactURL != "" {
		parsed, err := url.Parse(req.ArtifactURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "artifact_url must be an http or https URL"})
			return
		}
	}
	actorID, actorEmail := middleware.Actor(c)
	evidence := models.LabValidationEvidence{
		Kind: req.Kind, Subject: req.Subject, Status: req.Status, Summary: req.Summary,
		Details: req.Details, ArtifactURL: req.ArtifactURL, CreatedBy: actorEmail,
	}
	if err := h.attachLabEvidenceReferences(&evidence, req.RunID, req.ServerID, req.BootEventID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.Create(&evidence).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if evidence.ServerID != nil {
		h.workflow.ReconcileActiveDeploymentForServer(*evidence.ServerID)
	}
	h.audit.Record(actorID, actorEmail, "system.lab_validation.evidence", "lab_validation_evidence", evidence.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusCreated, labEvidenceFromModel(evidence))
}

func (h Handler) attachLabEvidenceReferences(evidence *models.LabValidationEvidence, runID *uint, serverID *uint, bootEventID *uint) error {
	if err := h.attachLabRunReference(evidence, runID); err != nil {
		return err
	}
	if serverID != nil && *serverID == 0 {
		return errors.New("server_id must be greater than 0")
	}
	if bootEventID != nil && *bootEventID == 0 {
		return errors.New("boot_event_id must be greater than 0")
	}
	if evidence.Status != "ok" {
		evidence.ServerID = serverID
		evidence.BootEventID = bootEventID
		return nil
	}
	switch evidence.Kind {
	case "pxe":
		event, err := h.requireLabBootEvent(bootEventID)
		if err != nil {
			return err
		}
		evidence.BootEventID = &event.ID
		evidence.ServerID = event.ServerID
		if serverID != nil {
			if !h.labBootEventBelongsToServer(event, *serverID) {
				return errors.New("boot_event_id must reference the same server_id or match the server primary_mac for PXE evidence")
			}
			evidence.ServerID = serverID
		}
		if !sameLabSubject(evidence.Subject, event.MAC) {
			return fmt.Errorf("subject must match boot event MAC %s", event.MAC)
		}
	case "bmc":
		endpoint, err := h.requireLabBMCOK(serverID)
		if err != nil {
			return err
		}
		if err := h.requireBMCEvidenceRun(evidence.RunID, endpoint.ServerID); err != nil {
			return err
		}
		evidence.ServerID = &endpoint.ServerID
		evidence.BmcEndpointID = &endpoint.ID
	case "ssh":
		access, err := h.requireLabSSHOK(serverID)
		if err != nil {
			return err
		}
		if err := h.requireSSHEvidenceRun(evidence.RunID, access.ServerID); err != nil {
			return err
		}
		evidence.ServerID = &access.ServerID
		evidence.SSHAccessID = &access.ID
	case "full":
		if serverID == nil {
			return errors.New("server_id is required for ok full-chain evidence")
		}
		event, err := h.requireLabBootEvent(bootEventID)
		if err != nil {
			return err
		}
		if !h.labBootEventBelongsToServer(event, *serverID) {
			return errors.New("boot_event_id must reference the same server_id or match the server primary_mac for full-chain evidence")
		}
		var endpoint *models.BmcEndpoint
		if h.labServerHasBMCEndpoint(*serverID) {
			checkedEndpoint, err := h.requireLabBMCOK(serverID)
			if err != nil {
				return err
			}
			endpoint = &checkedEndpoint
		}
		access, err := h.requireLabSSHOK(serverID)
		if err != nil {
			return err
		}
		if err := h.requireFullEvidenceRun(evidence.RunID, *serverID, event); err != nil {
			return err
		}
		evidence.ServerID = serverID
		evidence.BootEventID = &event.ID
		if endpoint != nil {
			evidence.BmcEndpointID = &endpoint.ID
		}
		evidence.SSHAccessID = &access.ID
	}
	return nil
}

func (h Handler) labBootEventBelongsToServer(event models.BootEvent, serverID uint) bool {
	if serverID == 0 {
		return false
	}
	if event.ServerID != nil && *event.ServerID == serverID {
		return true
	}
	var server models.Server
	if err := h.db.Select("primary_mac").First(&server, serverID).Error; err != nil {
		return false
	}
	return sameLabSubject(server.PrimaryMAC, event.MAC)
}

func (h Handler) attachLabRunReference(evidence *models.LabValidationEvidence, runID *uint) error {
	if runID == nil {
		return nil
	}
	if *runID == 0 {
		return errors.New("run_id must be greater than 0")
	}
	var run models.LabValidationRun
	if err := h.db.First(&run, *runID).Error; err != nil {
		return errors.New("run_id does not reference an existing lab validation run")
	}
	evidence.RunID = &run.ID
	if evidence.Status != "ok" {
		return nil
	}
	if run.StartedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return fmt.Errorf("run_id must reference a lab validation run from the last %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	if run.Status == "running" {
		return errors.New("run_id must reference a finished lab validation run")
	}
	return nil
}

func (h Handler) requireFullEvidenceRun(runID *uint, serverID uint, event models.BootEvent) error {
	if runID == nil || *runID == 0 {
		return errors.New("run_id is required for ok full-chain evidence")
	}
	var run models.LabValidationRun
	if err := h.db.First(&run, *runID).Error; err != nil {
		return errors.New("run_id does not reference an existing lab validation run")
	}
	if run.StartedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return fmt.Errorf("run_id must reference a lab validation run from the last %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	if run.Status == "running" || run.FinishedAt == nil {
		return errors.New("run_id must reference a finished lab validation run")
	}
	if !run.Strict || !run.CheckPXE || !run.CheckSSH {
		return errors.New("ok full-chain evidence requires a strict lab validation run that checked PXE and SSH")
	}
	bmcRequired := h.labServerHasBMCEndpoint(serverID)
	if bmcRequired && !run.CheckBMC {
		return errors.New("ok full-chain evidence for a target with BMC requires a strict lab validation run that checked BMC")
	}
	var serverIDs []uint
	_ = json.Unmarshal(run.ServerIDs, &serverIDs)
	if !uintSliceContains(serverIDs, serverID) {
		return fmt.Errorf("ok full-chain evidence requires run_id to include server_id %d", serverID)
	}
	var pxeMACs []string
	_ = json.Unmarshal(run.PXEMACs, &pxeMACs)
	if !labMACSliceContains(pxeMACs, event.MAC) {
		return fmt.Errorf("ok full-chain evidence requires run_id to include PXE MAC %s", event.MAC)
	}
	if !h.labRunHasSuccessfulResult(run.ID, "pxe_boot_event", serverID, event.MAC) {
		return errors.New("ok full-chain evidence requires the referenced run to include a successful PXE boot event result for this target")
	}
	if bmcRequired && !h.labRunHasBMCIdentityProof(run.ID, serverID) {
		return errors.New("ok full-chain evidence requires the referenced run to include a successful BMC result with structured identity proof details for this target")
	}
	if !h.labRunHasSSHCommandProof(run.ID, serverID) {
		return errors.New("ok full-chain evidence requires the referenced run to include a successful SSH result with known_hosts host key proof plus structured command, exit_code, and stdout details for this target")
	}
	return nil
}

func (h Handler) labServerHasBMCEndpoint(serverID uint) bool {
	if serverID == 0 {
		return false
	}
	var count int64
	h.db.Model(&models.BmcEndpoint{}).Where("server_id = ?", serverID).Count(&count)
	return count > 0
}

func (h Handler) requireBMCEvidenceRun(runID *uint, serverID uint) error {
	if runID == nil || *runID == 0 {
		return errors.New("run_id is required for ok BMC evidence")
	}
	var run models.LabValidationRun
	if err := h.db.First(&run, *runID).Error; err != nil {
		return errors.New("run_id does not reference an existing lab validation run")
	}
	if run.StartedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return fmt.Errorf("run_id must reference a lab validation run from the last %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	if run.Status == "running" || run.FinishedAt == nil {
		return errors.New("run_id must reference a finished lab validation run")
	}
	if !run.Strict || !run.CheckBMC {
		return errors.New("ok BMC evidence requires a strict lab validation run that checked BMC")
	}
	var serverIDs []uint
	_ = json.Unmarshal(run.ServerIDs, &serverIDs)
	if !uintSliceContains(serverIDs, serverID) {
		return fmt.Errorf("ok BMC evidence requires run_id to include server_id %d", serverID)
	}
	if !h.labRunHasBMCIdentityProof(run.ID, serverID) {
		return errors.New("ok BMC evidence requires the referenced run to include a successful BMC result with structured identity proof details for this target")
	}
	return nil
}

func (h Handler) requireSSHEvidenceRun(runID *uint, serverID uint) error {
	if runID == nil || *runID == 0 {
		return errors.New("run_id is required for ok SSH evidence")
	}
	var run models.LabValidationRun
	if err := h.db.First(&run, *runID).Error; err != nil {
		return errors.New("run_id does not reference an existing lab validation run")
	}
	if run.StartedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return fmt.Errorf("run_id must reference a lab validation run from the last %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	if run.Status == "running" || run.FinishedAt == nil {
		return errors.New("run_id must reference a finished lab validation run")
	}
	if !run.Strict || !run.CheckSSH {
		return errors.New("ok SSH evidence requires a strict lab validation run that checked SSH")
	}
	var serverIDs []uint
	_ = json.Unmarshal(run.ServerIDs, &serverIDs)
	if !uintSliceContains(serverIDs, serverID) {
		return fmt.Errorf("ok SSH evidence requires run_id to include server_id %d", serverID)
	}
	if !h.labRunHasSSHCommandProof(run.ID, serverID) {
		return errors.New("ok SSH evidence requires the referenced run to include a successful SSH result with known_hosts host key proof plus structured command, exit_code, and stdout details for this target")
	}
	return nil
}

func (h Handler) labRunHasSuccessfulResult(runID uint, kind string, serverID uint, mac string) bool {
	var rows []models.LabValidationRunResult
	if err := h.db.Where("run_id = ? AND kind = ? AND status = ?", runID, kind, "success").Find(&rows).Error; err != nil {
		return false
	}
	for _, row := range rows {
		if row.ServerID == serverID {
			return true
		}
		if mac != "" && (sameLabSubject(row.AssetNo, mac) || strings.Contains(strings.ToLower(row.Message), strings.ToLower(strings.TrimSpace(mac)))) {
			return true
		}
	}
	return false
}

func (h Handler) labRunHasBMCIdentityProof(runID uint, serverID uint) bool {
	var rows []models.LabValidationRunResult
	if err := h.db.Where("run_id = ? AND kind = ? AND status = ? AND server_id = ?", runID, "bmc", "success", serverID).Find(&rows).Error; err != nil {
		return false
	}
	for _, row := range rows {
		if labBMCResultHasIdentityProof(labValidationRunResultFromModel(row)) {
			return true
		}
	}
	return false
}

func (h Handler) labRunHasSSHCommandProof(runID uint, serverID uint) bool {
	var rows []models.LabValidationRunResult
	if err := h.db.Where("run_id = ? AND kind = ? AND status = ? AND server_id = ?", runID, "ssh", "success", serverID).Find(&rows).Error; err != nil {
		return false
	}
	for _, row := range rows {
		if labSSHResultHasCommandProof(labValidationRunResultFromModel(row)) {
			return true
		}
	}
	return false
}

func uintSliceContains(values []uint, expected uint) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func labMACSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if sameLabSubject(value, expected) {
			return true
		}
	}
	return false
}

func (h Handler) requireLabBootEvent(id *uint) (models.BootEvent, error) {
	if id == nil {
		return models.BootEvent{}, errors.New("boot_event_id is required for ok PXE evidence")
	}
	var event models.BootEvent
	if err := h.db.First(&event, *id).Error; err != nil {
		return models.BootEvent{}, errors.New("boot_event_id does not reference an existing boot event")
	}
	if event.CreatedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return models.BootEvent{}, fmt.Errorf("boot_event_id must reference a boot event from the last %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	if !labBootEventCountsAsPXEProof(event.Source) {
		return models.BootEvent{}, fmt.Errorf("boot_event_id source %s cannot prove physical PXE; use a recent http_ipxe or pxe_dhcp boot event", bootEventSourceLabel(event.Source))
	}
	return event, nil
}

func (h Handler) requireLabBMCOK(serverID *uint) (models.BmcEndpoint, error) {
	if serverID == nil {
		return models.BmcEndpoint{}, errors.New("server_id is required for ok BMC evidence")
	}
	if h.bmc.AdapterName() == "simulated" {
		return models.BmcEndpoint{}, errors.New("BMC_ADAPTER=simulated cannot produce ok physical BMC evidence")
	}
	var endpoint models.BmcEndpoint
	if err := h.db.Where("server_id = ?", *serverID).First(&endpoint).Error; err != nil {
		return models.BmcEndpoint{}, errors.New("server_id does not have a BMC endpoint")
	}
	if endpoint.Status != "ok" {
		return models.BmcEndpoint{}, fmt.Errorf("BMC endpoint for server_id %d is not ok", *serverID)
	}
	if issue := h.labBMCEndpointPolicyIssue(endpoint); issue != "" {
		return models.BmcEndpoint{}, fmt.Errorf("BMC endpoint for server_id %d cannot prove physical BMC: %s", *serverID, issue)
	}
	if endpoint.LastCheckedAt == nil || endpoint.LastCheckedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return models.BmcEndpoint{}, fmt.Errorf("BMC endpoint for server_id %d must have an ok check within %s", *serverID, formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	return endpoint, nil
}

func (h Handler) requireLabSSHOK(serverID *uint) (models.SSHAccess, error) {
	if serverID == nil {
		return models.SSHAccess{}, errors.New("server_id is required for ok SSH evidence")
	}
	if issue := h.labSSHPolicyIssue(); issue != "" {
		return models.SSHAccess{}, errors.New(issue)
	}
	var access models.SSHAccess
	if err := h.db.Where("server_id = ?", *serverID).First(&access).Error; err != nil {
		return models.SSHAccess{}, errors.New("server_id does not have SSH access")
	}
	if access.Status != "ok" {
		return models.SSHAccess{}, fmt.Errorf("SSH access for server_id %d is not ok", *serverID)
	}
	if access.LastCheckedAt == nil || access.LastCheckedAt.Before(time.Now().UTC().Add(-labEvidenceFreshnessWindow)) {
		return models.SSHAccess{}, fmt.Errorf("SSH access for server_id %d must have an ok check within %s", *serverID, formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	return access, nil
}

func sameLabSubject(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if leftMAC, leftErr := net.ParseMAC(left); leftErr == nil {
		if rightMAC, rightErr := net.ParseMAC(right); rightErr == nil {
			return strings.EqualFold(leftMAC.String(), rightMAC.String())
		}
	}
	return strings.EqualFold(left, right)
}

func (h Handler) runLabValidation(c *gin.Context) {
	var req labValidationRunOptions
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	opts, err := normalizeLabValidationRunOptions(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	runBMC := opts.CheckBMC == nil || *opts.CheckBMC
	runSSH := opts.CheckSSH == nil || *opts.CheckSSH
	runPXE := opts.CheckPXE == nil || *opts.CheckPXE

	actorID, actorEmail := middleware.Actor(c)
	run := models.LabValidationRun{
		Status: "running", Strict: opts.Strict, CheckPXE: runPXE, CheckBMC: runBMC, CheckSSH: runSSH,
		Limit: opts.Limit, ServerIDs: labJSONValue(opts.ServerIDs), PXEMACs: labJSONValue(opts.PXEMACs),
		PXEProbeMAC: opts.PXEProbeMAC, PXEArch: opts.pxeArch, SSHProbeCommand: opts.SSHProbeCommand, RequestedBy: actorEmail,
		RequestID: c.GetString("request_id"), StartedAt: time.Now().UTC(),
	}
	if err := h.db.Create(&run).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create lab validation run: " + err.Error()})
		return
	}

	results := []labValidationRunResult{}
	results = append(results, h.strictLabValidationGate(opts, runPXE, runBMC, runSSH)...)
	if runPXE {
		results = append(results, h.runLabPXEChecks(c.Request.Context(), opts)...)
	}
	if runBMC {
		results = append(results, h.runLabBMCChecks(c.Request.Context(), opts.Limit, opts.ServerIDs, opts.Strict)...)
	}
	if runSSH {
		results = append(results, h.runLabSSHChecks(c.Request.Context(), opts.Limit, opts.ServerIDs, opts.SSHProbeCommand)...)
	}
	if opts.Strict && runPXE && runSSH && len(opts.ServerIDs) > 0 {
		results = append(results, h.strictFullChainTargetResults(opts.ServerIDs)...)
	}
	if err := h.persistLabValidationRunResults(run.ID, results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist lab validation results: " + err.Error()})
		return
	}
	report := h.buildLabValidationReport(results, run.ID)
	finishedAt := time.Now().UTC()
	if err := h.db.Model(&models.LabValidationRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": report.Status, "finished_at": finishedAt}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "finish lab validation run: " + err.Error()})
		return
	}
	report = h.buildLabValidationReport(results, run.ID)
	h.audit.Record(actorID, actorEmail, "system.lab_validation.run", "lab_validation_run", run.ID, "medium", c.ClientIP(), c.Request.UserAgent(), c.GetString("request_id"))
	c.JSON(http.StatusOK, report)
}

func labJSONValue(value any) datatypes.JSON {
	data, err := json.Marshal(value)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(data)
}

func (h Handler) persistLabValidationRunResults(runID uint, results []labValidationRunResult) error {
	if len(results) == 0 {
		return nil
	}
	rows := make([]models.LabValidationRunResult, 0, len(results))
	for i := range results {
		results[i].RunID = runID
		if len([]rune(results[i].Message)) > 1000 {
			results[i].Message = string([]rune(results[i].Message)[:1000])
		}
		rows = append(rows, models.LabValidationRunResult{
			RunID: runID, Kind: results[i].Kind, ServerID: results[i].ServerID, Hostname: results[i].Hostname,
			AssetNo: results[i].AssetNo, Status: results[i].Status, Message: results[i].Message, Details: results[i].Details, CheckedAt: results[i].CheckedAt,
		})
	}
	if err := h.db.Create(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		results[i].ID = rows[i].ID
	}
	return nil
}

func normalizeLabValidationRunOptions(req labValidationRunOptions) (labValidationRunOptions, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	serverIDs, err := normalizeOptionalLabServerIDs(req.ServerIDs, 50)
	if err != nil {
		return req, err
	}
	req.ServerIDs = serverIDs
	pxeMACs, err := normalizeLabPXEMACs(req.PXEMACs, 20)
	if err != nil {
		return req, err
	}
	req.PXEMACs = pxeMACs
	if strings.TrimSpace(req.PXEProbeMAC) != "" {
		mac, err := net.ParseMAC(strings.TrimSpace(req.PXEProbeMAC))
		if err != nil || len(mac) != 6 {
			return req, errors.New("pxe_probe_mac must be a valid MAC address")
		}
		req.PXEProbeMAC = strings.ToLower(mac.String())
	}
	arch := uint16(9)
	if req.PXEArch != nil {
		arch = *req.PXEArch
	}
	switch arch {
	case 0, 7, 9, 11:
	default:
		return req, errors.New("pxe_arch must be one of 0, 7, 9, or 11")
	}
	req.pxeArch = arch
	req.SSHProbeCommand = strings.TrimSpace(req.SSHProbeCommand)
	if req.SSHProbeCommand == "" {
		req.SSHProbeCommand = services.DefaultSSHCheckCommand
	}
	if len([]rune(req.SSHProbeCommand)) > 255 {
		return req, errors.New("ssh_probe_command must be 255 characters or fewer")
	}
	if strings.ContainsAny(req.SSHProbeCommand, "\r\n\x00") {
		return req, errors.New("ssh_probe_command must be a single-line command")
	}
	return req, nil
}

func normalizeOptionalLabServerIDs(ids []uint, limit int) ([]uint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > limit {
		return nil, fmt.Errorf("server_ids cannot contain more than %d items", limit)
	}
	seen := map[uint]bool{}
	normalized := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("server_ids cannot contain 0")
		}
		if seen[id] {
			return nil, errors.New("server_ids cannot contain duplicates")
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized, nil
}

func normalizeLabPXEMACs(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("pxe_macs cannot contain more than %d items", limit)
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		mac, err := net.ParseMAC(strings.TrimSpace(value))
		if err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("invalid pxe_macs value %q", value)
		}
		text := strings.ToLower(mac.String())
		if seen[text] {
			return nil, fmt.Errorf("pxe_macs contains duplicate MAC %s", text)
		}
		seen[text] = true
		normalized = append(normalized, text)
	}
	return normalized, nil
}

func (h Handler) buildLabValidationReport(runResults []labValidationRunResult, runID uint) labValidationReport {
	report := labValidationReport{
		Status:      "ok",
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
		Environment: labValidationEnvironment{
			AppEnv:            h.cfg.AppEnv,
			BootBaseURL:       h.cfg.BootBaseURL,
			BMCAdapter:        h.bmc.AdapterName(),
			CollectorMode:     h.cfg.CollectorMode,
			SSHOperationsMode: h.cfg.SSHOperationsMode,
		},
		Checks:     []labValidationCheck{},
		RunResults: runResults,
	}
	h.fillPXELabValidation(&report)
	h.fillBMCLabValidation(&report)
	h.fillSSHLabValidation(&report)
	h.fillLabEvidence(&report)
	h.fillLabTargets(&report)
	h.fillLabValidationRuns(&report)
	report.addRunResultChecks(runResults)
	return report
}

func (r *labValidationReport) addCheck(name string, status string, message string) {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "warning"
	}
	if status == "error" {
		r.Status = "error"
	} else if status == "warning" && r.Status == "ok" {
		r.Status = "warning"
	}
	r.Checks = append(r.Checks, labValidationCheck{Name: name, Status: status, Message: message})
}

func (r *labValidationReport) addRunResultChecks(results []labValidationRunResult) {
	for _, result := range results {
		if !stringIn(result.Kind, "strict_physical_targets", "full_chain_target", "pxe_http", "pxe_dhcp", "pxe_tftp", "pxe_boot_event", "bmc", "ssh") {
			continue
		}
		status := "ok"
		if result.Status == "failed" {
			status = "error"
		} else if result.Status == "skipped" {
			status = "warning"
		}
		r.addCheck(result.Kind, status, result.Message)
	}
}

func (h Handler) strictLabValidationGate(opts labValidationRunOptions, runPXE bool, runBMC bool, runSSH bool) []labValidationRunResult {
	if !opts.Strict {
		return nil
	}
	results := []labValidationRunResult{}
	now := time.Now().UTC()
	addFailure := func(message string) {
		results = append(results, labValidationRunResult{Kind: "strict_physical_targets", Hostname: "Strict physical validation", AssetNo: "system", Status: "failed", Message: message, CheckedAt: &now})
	}
	if runPXE && len(opts.PXEMACs) == 0 {
		addFailure("strict physical validation requires at least one pxe_macs entry from a real PXE client")
	}
	if (runBMC || runSSH) && len(opts.ServerIDs) == 0 {
		addFailure("strict physical validation requires server_ids for BMC/SSH targets")
	}
	if runBMC && h.bmc.AdapterName() == "simulated" && h.labAnyRequestedServerHasBMCEndpoint(opts.ServerIDs) {
		addFailure("strict physical validation requires BMC_ADAPTER=redfish or ipmi when requested targets have BMC endpoints")
	}
	return results
}

func (h Handler) labAnyRequestedServerHasBMCEndpoint(serverIDs []uint) bool {
	if len(serverIDs) == 0 {
		var count int64
		h.db.Model(&models.BmcEndpoint{}).Limit(1).Count(&count)
		return count > 0
	}
	var count int64
	h.db.Model(&models.BmcEndpoint{}).Where("server_id IN ?", serverIDs).Limit(1).Count(&count)
	return count > 0
}

func (h Handler) fillLabEvidence(report *labValidationReport) {
	var rows []models.LabValidationEvidence
	h.db.Order("created_at desc, id desc").Limit(20).Find(&rows)
	report.RecentEvidence = make([]labValidationEvidence, 0, len(rows))
	okKinds := map[string]bool{}
	freshCutoff := time.Now().UTC().Add(-labEvidenceFreshnessWindow)
	errorCount := 0
	for _, row := range rows {
		report.RecentEvidence = append(report.RecentEvidence, labEvidenceFromModel(row))
		if h.labEvidenceCountsAsFreshOK(row, freshCutoff) {
			okKinds[row.Kind] = true
		}
		if row.Status == "error" {
			errorCount++
		}
	}
	if len(rows) == 0 {
		report.addCheck("physical_evidence", "warning", "no physical lab validation evidence has been recorded")
		return
	}
	if errorCount > 0 {
		report.addCheck("physical_evidence", "error", fmt.Sprintf("%d recorded physical evidence item(s) report error", errorCount))
		return
	}
	missing := []string{}
	for _, kind := range []string{"pxe", "ssh"} {
		if !okKinds[kind] && !okKinds["full"] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		report.addCheck("physical_evidence", "warning", fmt.Sprintf("missing fresh ok physical evidence within %s for %s", formatEvidenceWindow(labEvidenceFreshnessWindow), strings.Join(missing, ", ")))
		return
	}
	message := fmt.Sprintf("PXE and SSH physical evidence has been recorded within %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	if okKinds["bmc"] || okKinds["full"] {
		message = fmt.Sprintf("PXE, optional BMC where configured, and SSH physical evidence has been recorded within %s", formatEvidenceWindow(labEvidenceFreshnessWindow))
	}
	report.addCheck("physical_evidence", "ok", message)
}

func (h Handler) fillLabTargets(report *labValidationReport) {
	targets, readyCount := h.labValidationTargets()
	report.Targets = targets
	if len(targets) == 0 {
		report.addCheck("physical_targets", "warning", "no physical validation target has inventory MAC, BMC, SSH, PXE, or evidence records")
		return
	}
	if readyCount == 0 {
		report.addCheck("physical_targets", "warning", fmt.Sprintf("%d physical validation target(s) found, none are full-chain ready", len(report.Targets)))
		return
	}
	report.addCheck("physical_targets", "ok", fmt.Sprintf("%d/%d physical validation target(s) are full-chain ready", readyCount, len(report.Targets)))
}

func (h Handler) labValidationTargets() ([]labValidationTarget, int) {
	freshCutoff := time.Now().UTC().Add(-labEvidenceFreshnessWindow)
	targetIDs := map[uint]bool{}
	var endpoints []models.BmcEndpoint
	h.db.Order("updated_at desc, id desc").Limit(50).Find(&endpoints)
	endpointByServer := map[uint]models.BmcEndpoint{}
	for _, endpoint := range endpoints {
		if endpoint.ServerID == 0 {
			continue
		}
		targetIDs[endpoint.ServerID] = true
		if _, ok := endpointByServer[endpoint.ServerID]; !ok {
			endpointByServer[endpoint.ServerID] = endpoint
		}
	}
	var accesses []models.SSHAccess
	h.db.Order("updated_at desc, id desc").Limit(50).Find(&accesses)
	accessByServer := map[uint]models.SSHAccess{}
	for _, access := range accesses {
		if access.ServerID == 0 {
			continue
		}
		targetIDs[access.ServerID] = true
		if _, ok := accessByServer[access.ServerID]; !ok {
			accessByServer[access.ServerID] = access
		}
	}
	var events []models.BootEvent
	h.db.Order("created_at desc, id desc").Limit(100).Find(&events)
	eventByServer := map[uint]models.BootEvent{}
	eventByMAC := map[string]models.BootEvent{}
	for _, event := range events {
		if event.ServerID != nil && *event.ServerID > 0 {
			targetIDs[*event.ServerID] = true
			if current, ok := eventByServer[*event.ServerID]; !ok || labPreferredPXEBootEvent(event, current) {
				eventByServer[*event.ServerID] = event
			}
		}
		if mac := normalizeLabMAC(event.MAC); mac != "" {
			if current, ok := eventByMAC[mac]; !ok || labPreferredPXEBootEvent(event, current) {
				eventByMAC[mac] = event
			}
		}
	}
	var evidenceRows []models.LabValidationEvidence
	h.db.Where("server_id IS NOT NULL").Order("created_at desc, id desc").Limit(100).Find(&evidenceRows)
	evidenceByServer := map[uint][]models.LabValidationEvidence{}
	for _, evidence := range evidenceRows {
		if evidence.ServerID == nil || *evidence.ServerID == 0 {
			continue
		}
		targetIDs[*evidence.ServerID] = true
		evidenceByServer[*evidence.ServerID] = append(evidenceByServer[*evidence.ServerID], evidence)
	}
	var runResultRows []models.LabValidationRunResult
	h.db.Where("server_id <> 0").Order("created_at desc, id desc").Limit(200).Find(&runResultRows)
	latestRunResultByServer := map[uint]models.LabValidationRunResult{}
	runIDs := map[uint]bool{}
	for _, result := range runResultRows {
		if result.ServerID == 0 {
			continue
		}
		targetIDs[result.ServerID] = true
		runIDs[result.RunID] = true
		if _, ok := latestRunResultByServer[result.ServerID]; !ok {
			latestRunResultByServer[result.ServerID] = result
		}
	}
	runByID := map[uint]models.LabValidationRun{}
	if ids := labServerIDList(runIDs); len(ids) > 0 {
		var runRows []models.LabValidationRun
		h.db.Where("id IN ?", ids).Find(&runRows)
		for _, run := range runRows {
			runByID[run.ID] = run
		}
	}
	var inventoryTargets []models.Server
	h.db.Where("primary_mac <> ''").Order("updated_at desc, id desc").Limit(100).Find(&inventoryTargets)
	for _, server := range inventoryTargets {
		targetIDs[server.ID] = true
	}
	ids := labServerIDList(targetIDs)
	if len(ids) == 0 {
		return []labValidationTarget{}, 0
	}
	var servers []models.Server
	h.db.Where("id IN ?", ids).Order("id asc").Find(&servers)
	targets := make([]labValidationTarget, 0, len(servers))
	readyCount := 0
	for _, server := range servers {
		target := labValidationTarget{
			ServerID: server.ID, Hostname: server.Hostname, AssetNo: server.AssetNo, PrimaryMAC: server.PrimaryMAC, ServerStatus: server.Status,
			PXEStatus: "missing", BMCStatus: "missing", SSHStatus: "missing", EvidenceStatus: "missing", EvidenceKinds: []string{}, BlockingReasons: []string{},
		}
		event, eventOK := eventByServer[server.ID]
		if !eventOK {
			event, eventOK = eventByMAC[normalizeLabMAC(server.PrimaryMAC)]
		}
		if eventOK {
			target.PXEBootEventID = &event.ID
			target.PXEBootAt = &event.CreatedAt
			if event.CreatedAt.Before(freshCutoff) {
				target.PXEStatus = "stale"
			} else if !labBootEventCountsAsPXEProof(event.Source) {
				target.PXEStatus = "api_only"
			} else {
				target.PXEStatus = "ok"
			}
		}
		if endpoint, ok := endpointByServer[server.ID]; ok {
			target.BMCRequired = true
			target.BMCStatus = endpoint.Status
			target.BMCCheckedAt = endpoint.LastCheckedAt
			if target.BMCStatus == "ok" && h.labBMCEndpointPolicyIssue(endpoint) != "" {
				target.BMCStatus = "adapter_mismatch"
			}
			if target.BMCStatus == "ok" && (endpoint.LastCheckedAt == nil || endpoint.LastCheckedAt.Before(freshCutoff)) {
				target.BMCStatus = "stale"
			}
		}
		if access, ok := accessByServer[server.ID]; ok {
			target.SSHStatus = access.Status
			target.SSHCheckedAt = access.LastCheckedAt
			if access.Status == "ok" && h.labSSHPolicyIssue() != "" {
				target.SSHStatus = "policy_mismatch"
			}
			if access.Status == "ok" && (access.LastCheckedAt == nil || access.LastCheckedAt.Before(freshCutoff)) {
				target.SSHStatus = "stale"
			}
		}
		if result, ok := latestRunResultByServer[server.ID]; ok {
			runID := result.RunID
			target.LatestRunID = &runID
			target.LatestRunKind = result.Kind
			target.LatestRunResultStatus = result.Status
			if result.CheckedAt != nil {
				target.LatestRunAt = result.CheckedAt
			} else {
				checkedAt := result.CreatedAt
				target.LatestRunAt = &checkedAt
			}
			if run, ok := runByID[result.RunID]; ok {
				target.LatestRunStatus = run.Status
				target.LatestRunStrict = run.Strict
			}
		}
		target.EvidenceStatus, target.EvidenceID, target.EvidenceKinds = h.labTargetEvidenceStatus(evidenceByServer[server.ID], freshCutoff)
		if target.PXEStatus == "api_only" {
			target.BlockingReasons = append(target.BlockingReasons, "PXE boot event source is not HTTP iPXE or PXE DHCP")
		} else if target.PXEStatus != "ok" {
			target.BlockingReasons = append(target.BlockingReasons, "PXE boot event is not fresh")
		}
		if target.BMCRequired && h.bmc.AdapterName() == "simulated" {
			target.BlockingReasons = append(target.BlockingReasons, "BMC adapter is simulated for configured BMC endpoint")
		} else if target.BMCRequired && target.BMCStatus == "adapter_mismatch" {
			if endpoint, ok := endpointByServer[server.ID]; ok {
				target.BlockingReasons = append(target.BlockingReasons, h.labBMCEndpointPolicyIssue(endpoint))
			} else {
				target.BlockingReasons = append(target.BlockingReasons, "BMC endpoint does not match current adapter")
			}
		} else if target.BMCRequired && target.BMCStatus != "ok" {
			target.BlockingReasons = append(target.BlockingReasons, "BMC check is not fresh ok")
		}
		if target.SSHStatus == "policy_mismatch" {
			target.BlockingReasons = append(target.BlockingReasons, h.labSSHPolicyIssue())
		} else if target.SSHStatus != "ok" {
			target.BlockingReasons = append(target.BlockingReasons, "SSH check is not fresh ok")
		}
		if target.EvidenceStatus != "ok" {
			target.BlockingReasons = append(target.BlockingReasons, "full-chain evidence is not fresh ok")
		}
		target.FullChainReady = len(target.BlockingReasons) == 0
		if target.FullChainReady {
			readyCount++
		}
		targets = append(targets, target)
	}
	return targets, readyCount
}

func (h Handler) strictFullChainTargetResults(serverIDs []uint) []labValidationRunResult {
	if len(serverIDs) == 0 {
		return nil
	}
	targets, _ := h.labValidationTargets()
	byServer := map[uint]labValidationTarget{}
	for _, target := range targets {
		byServer[target.ServerID] = target
	}
	now := time.Now().UTC()
	results := make([]labValidationRunResult, 0, len(serverIDs))
	for _, serverID := range serverIDs {
		ref := h.labServerRef(serverID)
		result := labValidationRunResult{
			Kind:      "full_chain_target",
			ServerID:  serverID,
			Hostname:  ref.Hostname,
			AssetNo:   ref.AssetNo,
			Status:    "failed",
			CheckedAt: &now,
		}
		target, ok := byServer[serverID]
		if !ok {
			result.Message = "server is not a physical validation target; configure inventory MAC, BMC endpoint, SSH access, or referenced evidence"
			results = append(results, result)
			continue
		}
		result.Hostname = target.Hostname
		result.AssetNo = target.AssetNo
		if target.FullChainReady {
			result.Status = "success"
			if target.BMCRequired {
				result.Message = fmt.Sprintf("full-chain target is ready: PXE BootEvent #%d, BMC ok, SSH ok, and fresh full-chain evidence are present", valueOrZero(target.PXEBootEventID))
			} else {
				result.Message = fmt.Sprintf("full-chain target is ready: PXE BootEvent #%d, SSH ok, and fresh full-chain evidence are present; BMC is not configured for this target", valueOrZero(target.PXEBootEventID))
			}
		} else {
			result.Message = "full-chain target is not ready: " + strings.Join(target.BlockingReasons, "; ")
		}
		results = append(results, result)
	}
	return results
}

func normalizeLabMAC(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	mac, err := net.ParseMAC(value)
	if err != nil || len(mac) != 6 {
		return strings.ToLower(value)
	}
	return strings.ToLower(mac.String())
}

func (h Handler) labTargetEvidenceStatus(rows []models.LabValidationEvidence, freshCutoff time.Time) (string, *uint, []string) {
	if len(rows) == 0 {
		return "missing", nil, []string{}
	}
	kinds := []string{}
	seen := map[string]bool{}
	status := "stale"
	var evidenceID *uint
	for _, row := range rows {
		if !seen[row.Kind] {
			seen[row.Kind] = true
			kinds = append(kinds, row.Kind)
		}
		if row.Kind == "full" && h.labEvidenceCountsAsFreshOK(row, freshCutoff) {
			id := row.ID
			return "ok", &id, kinds
		}
		if h.labEvidenceCountsAsFreshOK(row, freshCutoff) && status != "ok" {
			id := row.ID
			evidenceID = &id
			status = "partial"
		}
	}
	return status, evidenceID, kinds
}

func (h Handler) fillLabValidationRuns(report *labValidationReport) {
	var rows []models.LabValidationRun
	h.db.Order("started_at desc, id desc").Limit(10).Find(&rows)
	report.RecentRuns = make([]labValidationRunSummary, 0, len(rows))
	for _, row := range rows {
		summary := labValidationRunFromModel(row)
		h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ?", row.ID).Count(&summary.Results)
		h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ? AND status = ?", row.ID, "failed").Count(&summary.Failures)
		h.db.Model(&models.LabValidationRunResult{}).Where("run_id = ? AND status = ?", row.ID, "skipped").Count(&summary.Skipped)
		report.RecentRuns = append(report.RecentRuns, summary)
	}
	if len(rows) == 0 {
		report.addCheck("lab_validation_runs", "warning", "no lab validation run has been executed")
		return
	}
	latest := rows[0]
	switch latest.Status {
	case "ok":
		report.addCheck("lab_validation_runs", "ok", fmt.Sprintf("latest lab validation run %d passed", latest.ID))
	case "warning":
		report.addCheck("lab_validation_runs", "warning", fmt.Sprintf("latest lab validation run %d completed with warnings", latest.ID))
	case "error":
		report.addCheck("lab_validation_runs", "error", fmt.Sprintf("latest lab validation run %d completed with errors", latest.ID))
	case "running":
		if latest.ID == report.RunID {
			report.addCheck("lab_validation_runs", "ok", fmt.Sprintf("current lab validation run %d is being recorded", latest.ID))
		} else {
			report.addCheck("lab_validation_runs", "warning", fmt.Sprintf("latest lab validation run %d is still marked running", latest.ID))
		}
	default:
		report.addCheck("lab_validation_runs", "warning", fmt.Sprintf("latest lab validation run %d has status %q", latest.ID, latest.Status))
	}
}

func labRunServerIDSet(detail labValidationRunDetail) map[uint]bool {
	ids := map[uint]bool{}
	for _, id := range detail.Run.ServerIDs {
		if id > 0 {
			ids[id] = true
		}
	}
	for _, result := range detail.Results {
		if result.ServerID > 0 {
			ids[result.ServerID] = true
		}
	}
	return ids
}

func labServerIDList(ids map[uint]bool) []uint {
	values := make([]uint, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	return values
}

func (h Handler) labBootEventsForBundle(detail labValidationRunDetail) []labBootEvent {
	macs := make([]string, 0, len(detail.Run.PXEMACs))
	for _, mac := range detail.Run.PXEMACs {
		macs = append(macs, strings.ToLower(strings.TrimSpace(mac)))
	}
	serverIDs := labServerIDList(labRunServerIDSet(detail))
	var rows []models.BootEvent
	query := h.db.Order("created_at desc, id desc").Limit(50)
	switch {
	case len(macs) > 0 && len(serverIDs) > 0:
		query = query.Where("LOWER(mac) IN ? OR server_id IN ?", macs, serverIDs)
	case len(macs) > 0:
		query = query.Where("LOWER(mac) IN ?", macs)
	case len(serverIDs) > 0:
		query = query.Where("server_id IN ?", serverIDs)
	default:
		return []labBootEvent{}
	}
	query.Find(&rows)
	events := make([]labBootEvent, 0, len(rows))
	for _, event := range rows {
		events = append(events, labBootEvent{
			ID: event.ID, MAC: event.MAC, Architecture: event.Architecture, Firmware: event.Firmware,
			RemoteAddr: event.RemoteAddr, Source: event.Source, ServerID: event.ServerID, DeploymentID: event.DeploymentID, CreatedAt: event.CreatedAt,
		})
	}
	return events
}

func (h Handler) labBMCEndpointsForBundle(serverIDs map[uint]bool) []labBMCRef {
	ids := labServerIDList(serverIDs)
	if len(ids) == 0 {
		return []labBMCRef{}
	}
	var endpoints []models.BmcEndpoint
	h.db.Where("server_id IN ?", ids).Order("updated_at desc, id desc").Find(&endpoints)
	refs := make([]labBMCRef, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ref := h.labServerRef(endpoint.ServerID)
		refs = append(refs, labBMCRef{
			ServerID: endpoint.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Type: endpoint.Type, Protocol: endpoint.Protocol,
			Endpoint: endpoint.Endpoint, Status: endpoint.Status, PowerState: endpoint.PowerState, LastCheckedAt: endpoint.LastCheckedAt, UpdatedAt: endpoint.UpdatedAt,
		})
	}
	return refs
}

func (h Handler) labSSHAccessesForBundle(serverIDs map[uint]bool) []labSSHRef {
	ids := labServerIDList(serverIDs)
	if len(ids) == 0 {
		return []labSSHRef{}
	}
	var accesses []models.SSHAccess
	h.db.Where("server_id IN ?", ids).Order("updated_at desc, id desc").Find(&accesses)
	refs := make([]labSSHRef, 0, len(accesses))
	for _, access := range accesses {
		ref := h.labServerRef(access.ServerID)
		refs = append(refs, labSSHRef{
			ServerID: access.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Host: access.Host, Port: access.Port,
			Username: access.Username, AuthType: access.AuthType, Status: access.Status, LastCheckedAt: access.LastCheckedAt, UpdatedAt: access.UpdatedAt,
		})
	}
	return refs
}

func (h Handler) labTerminalSessionsForBundle(serverIDs map[uint]bool) []labTerminalSessionRef {
	ids := labServerIDList(serverIDs)
	if len(ids) == 0 {
		return []labTerminalSessionRef{}
	}
	var sessions []models.TerminalSession
	h.db.Where("server_id IN ?", ids).Order("opened_at desc, id desc").Limit(20).Find(&sessions)
	refs := make([]labTerminalSessionRef, 0, len(sessions))
	for _, session := range sessions {
		ref := h.labServerRef(session.ServerID)
		refs = append(refs, labTerminalSessionRef{
			ID: session.ID, ServerID: session.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo,
			Status: session.Status, Mode: session.Mode, RequestedBy: session.RequestedBy, Reason: session.Reason,
			Transcript: session.Transcript, OpenedAt: session.OpenedAt, ClosedAt: session.ClosedAt, CreatedAt: session.CreatedAt,
		})
	}
	return refs
}

func (h Handler) labScriptExecutionsForBundle(serverIDs map[uint]bool) []labScriptExecutionRef {
	ids := labServerIDList(serverIDs)
	if len(ids) == 0 {
		return []labScriptExecutionRef{}
	}
	var executions []models.ScriptExecution
	h.db.Where("server_id IN ?", ids).Order("finished_at desc, id desc").Limit(50).Find(&executions)
	jobIDs := make([]uint, 0, len(executions))
	seenJobs := map[uint]bool{}
	for _, execution := range executions {
		if execution.ScriptJobID == 0 || seenJobs[execution.ScriptJobID] {
			continue
		}
		seenJobs[execution.ScriptJobID] = true
		jobIDs = append(jobIDs, execution.ScriptJobID)
	}
	jobNames := map[uint]string{}
	if len(jobIDs) > 0 {
		var jobs []models.ScriptJob
		h.db.Select("id", "name").Where("id IN ?", jobIDs).Find(&jobs)
		for _, job := range jobs {
			jobNames[job.ID] = job.Name
		}
	}
	refs := make([]labScriptExecutionRef, 0, len(executions))
	for _, execution := range executions {
		ref := h.labServerRef(execution.ServerID)
		refs = append(refs, labScriptExecutionRef{
			ID: execution.ID, ScriptJobID: execution.ScriptJobID, ServerID: execution.ServerID,
			Hostname: ref.Hostname, AssetNo: ref.AssetNo, JobName: jobNames[execution.ScriptJobID],
			Status: execution.Status, ExitCode: execution.ExitCode, Stdout: execution.Stdout, Stderr: execution.Stderr,
			StartedAt: execution.StartedAt, FinishedAt: execution.FinishedAt, CreatedAt: execution.CreatedAt,
		})
	}
	return refs
}

func (h Handler) labLogEventsForBundle(serverIDs map[uint]bool) []labLogEventRef {
	ids := labServerIDList(serverIDs)
	if len(ids) == 0 {
		return []labLogEventRef{}
	}
	var events []models.LogEvent
	h.db.Where("server_id IN ?", ids).Order("occurred_at desc, id desc").Limit(50).Find(&events)
	refs := make([]labLogEventRef, 0, len(events))
	for _, event := range events {
		ref := h.labServerRef(event.ServerID)
		refs = append(refs, labLogEventRef{
			ID: event.ID, ServerID: event.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo,
			Source: event.Source, Level: event.Level, Message: event.Message, TraceID: event.TraceID,
			OccurredAt: event.OccurredAt, CreatedAt: event.CreatedAt,
		})
	}
	return refs
}

func (h Handler) labEvidenceForBundle(detail labValidationRunDetail, bootEvents []labBootEvent, serverIDs map[uint]bool) []labValidationEvidence {
	eventIDs := make([]uint, 0, len(bootEvents))
	for _, event := range bootEvents {
		eventIDs = append(eventIDs, event.ID)
	}
	ids := labServerIDList(serverIDs)
	macs := make([]string, 0, len(detail.Run.PXEMACs))
	for _, mac := range detail.Run.PXEMACs {
		macs = append(macs, strings.ToLower(strings.TrimSpace(mac)))
	}
	var rows []models.LabValidationEvidence
	query := h.db.Order("created_at desc, id desc").Limit(50)
	runID := detail.Run.ID
	switch {
	case len(ids) > 0 && len(eventIDs) > 0 && len(macs) > 0:
		query = query.Where("run_id = ? OR server_id IN ? OR boot_event_id IN ? OR LOWER(subject) IN ?", runID, ids, eventIDs, macs)
	case len(ids) > 0 && len(eventIDs) > 0:
		query = query.Where("run_id = ? OR server_id IN ? OR boot_event_id IN ?", runID, ids, eventIDs)
	case len(ids) > 0 && len(macs) > 0:
		query = query.Where("run_id = ? OR server_id IN ? OR LOWER(subject) IN ?", runID, ids, macs)
	case len(eventIDs) > 0 && len(macs) > 0:
		query = query.Where("run_id = ? OR boot_event_id IN ? OR LOWER(subject) IN ?", runID, eventIDs, macs)
	case len(ids) > 0:
		query = query.Where("run_id = ? OR server_id IN ?", runID, ids)
	case len(eventIDs) > 0:
		query = query.Where("run_id = ? OR boot_event_id IN ?", runID, eventIDs)
	case len(macs) > 0:
		query = query.Where("run_id = ? OR LOWER(subject) IN ?", runID, macs)
	default:
		query = query.Where("run_id = ?", runID)
	}
	query.Find(&rows)
	evidence := make([]labValidationEvidence, 0, len(rows))
	for _, row := range rows {
		evidence = append(evidence, labEvidenceFromModel(row))
	}
	return evidence
}

func formatEvidenceWindow(window time.Duration) string {
	days := int(window.Hours() / 24)
	if days > 0 && window == time.Duration(days)*24*time.Hour {
		return fmt.Sprintf("%dd", days)
	}
	return window.String()
}

func labValidationRunFromModel(row models.LabValidationRun) labValidationRunSummary {
	summary := labValidationRunSummary{
		ID: row.ID, Status: row.Status, Strict: row.Strict, CheckPXE: row.CheckPXE, CheckBMC: row.CheckBMC, CheckSSH: row.CheckSSH,
		Limit: row.Limit, PXEProbeMAC: row.PXEProbeMAC, PXEArch: row.PXEArch, SSHProbeCommand: row.SSHProbeCommand, RequestedBy: row.RequestedBy,
		RequestID: row.RequestID, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
	_ = json.Unmarshal(row.ServerIDs, &summary.ServerIDs)
	_ = json.Unmarshal(row.PXEMACs, &summary.PXEMACs)
	return summary
}

func labValidationRunResultFromModel(row models.LabValidationRunResult) labValidationRunResult {
	return labValidationRunResult{
		ID: row.ID, RunID: row.RunID, Kind: row.Kind, ServerID: row.ServerID, Hostname: row.Hostname,
		AssetNo: row.AssetNo, Status: row.Status, Message: row.Message, Details: row.Details, CheckedAt: row.CheckedAt,
	}
}

func labEvidenceFromModel(row models.LabValidationEvidence) labValidationEvidence {
	return labValidationEvidence{
		ID: row.ID, Kind: row.Kind, Subject: row.Subject, Status: row.Status, Summary: row.Summary,
		Details: row.Details, ArtifactURL: row.ArtifactURL, RunID: row.RunID, ServerID: row.ServerID, BootEventID: row.BootEventID,
		BmcEndpointID: row.BmcEndpointID, SSHAccessID: row.SSHAccessID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}
}

func (h Handler) labEvidenceCountsAsFreshOK(row models.LabValidationEvidence, freshCutoff time.Time) bool {
	if row.Status != "ok" || row.CreatedAt.Before(freshCutoff) {
		return false
	}
	switch row.Kind {
	case "pxe":
		return h.labEvidenceBootEventHasPhysicalSource(row)
	case "bmc":
		return h.labEvidenceBMCReferenceOK(row, freshCutoff) && h.labEvidenceBMCRunOK(row)
	case "ssh":
		return h.labEvidenceSSHReferenceOK(row, freshCutoff) && h.labEvidenceSSHRunOK(row)
	case "full":
		return h.labEvidenceBootEventHasPhysicalSource(row) && h.labEvidenceFullBMCReferenceOK(row, freshCutoff) && h.labEvidenceSSHReferenceOK(row, freshCutoff) && h.labEvidenceFullRunOK(row)
	default:
		return false
	}
}

func (h Handler) labEvidenceFullRunOK(row models.LabValidationEvidence) bool {
	if row.RunID == nil || row.ServerID == nil || row.BootEventID == nil {
		return false
	}
	var event models.BootEvent
	if err := h.db.First(&event, *row.BootEventID).Error; err != nil {
		return false
	}
	return h.requireFullEvidenceRun(row.RunID, *row.ServerID, event) == nil
}

func (h Handler) labEvidenceBMCRunOK(row models.LabValidationEvidence) bool {
	if row.RunID == nil || row.ServerID == nil {
		return false
	}
	return h.requireBMCEvidenceRun(row.RunID, *row.ServerID) == nil
}

func (h Handler) labEvidenceSSHRunOK(row models.LabValidationEvidence) bool {
	if row.RunID == nil || row.ServerID == nil {
		return false
	}
	return h.requireSSHEvidenceRun(row.RunID, *row.ServerID) == nil
}

func (h Handler) labEvidenceBootEventHasPhysicalSource(row models.LabValidationEvidence) bool {
	if row.BootEventID == nil || *row.BootEventID == 0 {
		return false
	}
	var event models.BootEvent
	if err := h.db.Select("id", "source").First(&event, *row.BootEventID).Error; err != nil {
		return false
	}
	return labBootEventCountsAsPXEProof(event.Source)
}

func (h Handler) labEvidenceBMCReferenceOK(row models.LabValidationEvidence, freshCutoff time.Time) bool {
	if row.ServerID == nil || *row.ServerID == 0 || row.BmcEndpointID == nil || *row.BmcEndpointID == 0 {
		return false
	}
	var endpoint models.BmcEndpoint
	if err := h.db.First(&endpoint, *row.BmcEndpointID).Error; err != nil {
		return false
	}
	if endpoint.ServerID != *row.ServerID || endpoint.Status != "ok" || endpoint.LastCheckedAt == nil || endpoint.LastCheckedAt.Before(freshCutoff) {
		return false
	}
	return h.labBMCEndpointMatchesAdapter(endpoint)
}

func (h Handler) labEvidenceFullBMCReferenceOK(row models.LabValidationEvidence, freshCutoff time.Time) bool {
	if row.ServerID == nil || *row.ServerID == 0 {
		return false
	}
	if !h.labServerHasBMCEndpoint(*row.ServerID) {
		return true
	}
	return h.labEvidenceBMCReferenceOK(row, freshCutoff)
}

func (h Handler) labEvidenceSSHReferenceOK(row models.LabValidationEvidence, freshCutoff time.Time) bool {
	if row.ServerID == nil || *row.ServerID == 0 || row.SSHAccessID == nil || *row.SSHAccessID == 0 {
		return false
	}
	var access models.SSHAccess
	if err := h.db.First(&access, *row.SSHAccessID).Error; err != nil {
		return false
	}
	if access.ServerID != *row.ServerID || access.Status != "ok" || access.LastCheckedAt == nil || access.LastCheckedAt.Before(freshCutoff) {
		return false
	}
	return true
}

func (h Handler) labBMCEndpointMatchesAdapter(endpoint models.BmcEndpoint) bool {
	return h.labBMCEndpointPolicyIssue(endpoint) == ""
}

func (h Handler) labBMCEndpointPolicyIssue(endpoint models.BmcEndpoint) string {
	adapter := strings.ToLower(strings.TrimSpace(h.bmc.AdapterName()))
	if adapter == "simulated" {
		return "BMC_ADAPTER=simulated cannot prove physical Redfish/IPMI validation"
	}
	if !strings.EqualFold(strings.TrimSpace(endpoint.Type), adapter) {
		return fmt.Sprintf("BMC endpoint type %q does not match current BMC_ADAPTER %q", endpoint.Type, h.bmc.AdapterName())
	}
	if adapter == "redfish" {
		parsed, err := url.Parse(strings.TrimSpace(endpoint.Endpoint))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "Redfish endpoint must be an http or https URL"
		}
		protocol := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
		if protocol != "" && protocol != parsed.Scheme {
			return "Redfish protocol must match endpoint URL scheme"
		}
		if config.IsProduction(h.cfg.AppEnv) && parsed.Scheme != "https" {
			return "production Redfish endpoint must use https"
		}
	}
	return ""
}

func (h Handler) fillPXELabValidation(report *labValidationReport) {
	pxe := labValidationPXE{
		Enabled:        h.cfg.BootServicesEnabled,
		Mode:           h.cfg.BootServiceMode,
		BindInterface:  h.cfg.BootBindInterface,
		DHCPListenAddr: h.cfg.BootDHCPListenAddr,
		DHCPServerIP:   h.cfg.BootDHCPServerIP,
		DHCPLeaseStart: h.cfg.BootDHCPLeaseStart,
		DHCPLeaseEnd:   h.cfg.BootDHCPLeaseEnd,
		TFTPListenAddr: h.cfg.BootTFTPListenAddr,
		TFTPRoot:       h.cfg.BootTFTPRoot,
		BootfileUEFI:   h.cfg.BootTFTPBootfileUEFI,
		BootfileBIOS:   h.cfg.BootTFTPBootfileBIOS,
		RuntimeIssues:  config.BootRuntimeIssues(h.cfg),
	}
	if strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) == "proxy" {
		if proxyAddr, err := services.ProxyDHCPBootServerListenAddr(h.cfg.BootDHCPListenAddr); err == nil {
			pxe.ProxyDHCPListenAddr = proxyAddr
		}
	}
	h.db.Model(&models.NetworkConfig{}).Where("purpose = ? AND status = ?", "deployment", "enabled").Count(&pxe.DeploymentNetworks)
	h.db.Model(&models.BootEvent{}).Count(&pxe.BootEvents)
	var events []models.BootEvent
	h.db.Order("created_at desc, id desc").Limit(10).Find(&events)
	pxe.RecentBootEvents = make([]labBootEvent, 0, len(events))
	for _, event := range events {
		pxe.RecentBootEvents = append(pxe.RecentBootEvents, labBootEvent{
			ID: event.ID, MAC: event.MAC, Architecture: event.Architecture, Firmware: event.Firmware,
			RemoteAddr: event.RemoteAddr, Source: event.Source, ServerID: event.ServerID, DeploymentID: event.DeploymentID, CreatedAt: event.CreatedAt,
		})
	}
	report.PXE = pxe

	if !pxe.Enabled {
		report.addCheck("pxe_services", "warning", "PXE/DHCP/TFTP listeners are disabled")
	} else {
		status := "ok"
		message := "PXE/DHCP/TFTP runtime checks passed"
		for _, issue := range pxe.RuntimeIssues {
			if issue.Level == "error" {
				status = "error"
				message = issue.Message
				break
			}
			if issue.Level == "warning" && status == "ok" {
				status = "warning"
				message = issue.Message
			}
		}
		report.addCheck("pxe_services", status, message)
	}
	if pxe.DeploymentNetworks == 0 {
		report.addCheck("deployment_network", "warning", "no enabled deployment network is configured")
	} else {
		report.addCheck("deployment_network", "ok", fmt.Sprintf("%d enabled deployment network(s)", pxe.DeploymentNetworks))
	}
	if pxe.BootEvents == 0 {
		report.addCheck("pxe_boot_events", "warning", "no PXE boot event has been recorded yet")
	} else {
		report.addCheck("pxe_boot_events", "ok", fmt.Sprintf("%d PXE boot event(s) recorded", pxe.BootEvents))
	}
}

func (h Handler) fillBMCLabValidation(report *labValidationReport) {
	bmc := labValidationBMC{Adapter: h.bmc.AdapterName()}
	h.db.Model(&models.BmcEndpoint{}).Count(&bmc.Total)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ?", "ok").Count(&bmc.OK)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ?", "error").Count(&bmc.Error)
	h.db.Model(&models.BmcEndpoint{}).Where("status = ? OR status = ''", "unknown").Count(&bmc.Unknown)
	var latest models.BmcEndpoint
	if err := h.db.Where("last_checked_at IS NOT NULL").Order("last_checked_at desc").First(&latest).Error; err == nil {
		bmc.LastCheckedAt = latest.LastCheckedAt
	}
	var endpoints []models.BmcEndpoint
	h.db.Order("updated_at desc, id desc").Find(&endpoints)
	policyIssues := 0
	bmc.RecentEndpoints = make([]labBMCRef, 0, len(endpoints))
	for idx, endpoint := range endpoints {
		if bmc.Adapter != "simulated" && h.labBMCEndpointPolicyIssue(endpoint) != "" {
			policyIssues++
		}
		if idx >= 10 {
			continue
		}
		ref := h.labServerRef(endpoint.ServerID)
		bmc.RecentEndpoints = append(bmc.RecentEndpoints, labBMCRef{
			ServerID: endpoint.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Type: endpoint.Type, Protocol: endpoint.Protocol,
			Endpoint: endpoint.Endpoint, Status: endpoint.Status, PowerState: endpoint.PowerState, LastCheckedAt: endpoint.LastCheckedAt, UpdatedAt: endpoint.UpdatedAt,
		})
	}
	report.BMC = bmc

	if bmc.Adapter == "simulated" {
		report.addCheck("bmc_adapter", "warning", "BMC_ADAPTER=simulated; physical Redfish/IPMI checks are not active")
	} else {
		report.addCheck("bmc_adapter", "ok", "physical BMC adapter is configured: "+bmc.Adapter)
	}
	toolingStatus, toolingMessage := bmcToolingStatus(bmc.Adapter)
	report.addCheck("bmc_tooling", toolingStatus, toolingMessage)
	switch {
	case bmc.Total == 0:
		report.addCheck("bmc_connectivity", "warning", "no BMC endpoint is configured")
	case policyIssues > 0:
		report.addCheck("bmc_connectivity", "error", fmt.Sprintf("%d BMC endpoint(s) violate the active adapter or Redfish security policy", policyIssues))
	case bmc.Error > 0:
		report.addCheck("bmc_connectivity", "error", fmt.Sprintf("%d BMC endpoint(s) are in error", bmc.Error))
	case bmc.Unknown > 0:
		report.addCheck("bmc_connectivity", "warning", fmt.Sprintf("%d BMC endpoint(s) have not passed connectivity check", bmc.Unknown))
	default:
		report.addCheck("bmc_connectivity", "ok", fmt.Sprintf("%d BMC endpoint(s) passed connectivity check", bmc.OK))
	}
}

func (h Handler) fillSSHLabValidation(report *labValidationReport) {
	ssh := labValidationSSH{CollectorMode: h.cfg.CollectorMode, OperationsMode: h.cfg.SSHOperationsMode}
	h.db.Model(&models.SSHAccess{}).Count(&ssh.Total)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "ok").Count(&ssh.OK)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "error").Count(&ssh.Error)
	h.db.Model(&models.SSHAccess{}).Where("status = ?", "configured").Count(&ssh.Configured)
	h.db.Model(&models.SSHAccess{}).Where("status = ? OR status = ''", "unknown").Count(&ssh.Unknown)
	var latest models.SSHAccess
	if err := h.db.Where("last_checked_at IS NOT NULL").Order("last_checked_at desc").First(&latest).Error; err == nil {
		ssh.LastCheckedAt = latest.LastCheckedAt
	}
	var accesses []models.SSHAccess
	h.db.Order("updated_at desc, id desc").Limit(10).Find(&accesses)
	ssh.RecentSSHAccesses = make([]labSSHRef, 0, len(accesses))
	for _, access := range accesses {
		ref := h.labServerRef(access.ServerID)
		ssh.RecentSSHAccesses = append(ssh.RecentSSHAccesses, labSSHRef{
			ServerID: access.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Host: access.Host, Port: access.Port,
			Username: access.Username, AuthType: access.AuthType, Status: access.Status, LastCheckedAt: access.LastCheckedAt, UpdatedAt: access.UpdatedAt,
		})
	}
	report.SSH = ssh

	if issue := h.labSSHPolicyIssue(); issue != "" {
		report.addCheck("ssh_modes", "error", issue)
	} else if strings.ToLower(strings.TrimSpace(ssh.CollectorMode)) != "ssh" && strings.ToLower(strings.TrimSpace(ssh.OperationsMode)) != "ssh" {
		report.addCheck("ssh_modes", "warning", "COLLECTOR_MODE and SSH_OPERATIONS_MODE are not using ssh")
	} else {
		report.addCheck("ssh_modes", "ok", "SSH-backed collection or operations mode is enabled")
	}
	knownHostsStatus, knownHostsMessage := h.sshKnownHostsStatus()
	report.addCheck("ssh_known_hosts", knownHostsStatus, knownHostsMessage)
	switch {
	case ssh.Total == 0:
		report.addCheck("ssh_connectivity", "warning", "no SSH access is configured")
	case ssh.Error > 0:
		report.addCheck("ssh_connectivity", "error", fmt.Sprintf("%d SSH target(s) are in error", ssh.Error))
	case ssh.Configured+ssh.Unknown > 0:
		report.addCheck("ssh_connectivity", "warning", fmt.Sprintf("%d SSH target(s) have not passed connectivity check", ssh.Configured+ssh.Unknown))
	default:
		report.addCheck("ssh_connectivity", "ok", fmt.Sprintf("%d SSH target(s) passed connectivity check", ssh.OK))
	}
}

func (h Handler) runLabPXEChecks(parent context.Context, opts labValidationRunOptions) []labValidationRunResult {
	results := make([]labValidationRunResult, 0, 3)
	probeMAC := opts.PXEProbeMAC
	if probeMAC == "" {
		probeMAC = "52:54:00:00:00:fe"
	}
	results = append(results, h.runLabPXEHTTPCheck(parent, probeMAC))
	results = append(results, h.runLabPXEDHCPCheck(parent, probeMAC, opts.pxeArch))
	results = append(results, h.runLabPXETFTPCheck(parent))
	for _, mac := range opts.PXEMACs {
		results = append(results, h.runLabPXEBootEventCheck(mac, opts.ServerIDs...))
	}
	return results
}

func (h Handler) runLabPXEHTTPCheck(parent context.Context, probeMAC string) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_http", Hostname: "PXE HTTP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	probeURL, err := labIPXEProbeURL(h.cfg.BootBaseURL, probeMAC)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	req.Header.Set(labValidationHTTPProbeHeader, "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		result.Message = fmt.Sprintf("BOOT_BASE_URL /boot/ipxe returned HTTP %d", res.StatusCode)
		return result
	}
	if !strings.Contains(string(body), "#!ipxe") {
		result.Message = "BOOT_BASE_URL /boot/ipxe did not return an iPXE script"
		return result
	}
	result.Status = "success"
	result.Message = "BOOT_BASE_URL /boot/ipxe returned a valid iPXE script"
	return result
}

func (h Handler) runLabPXEDHCPCheck(parent context.Context, probeMAC string, arch uint16) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_dhcp", Hostname: "PXE DHCP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	if !h.cfg.BootServicesEnabled {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICES_ENABLED=false; DHCP/ProxyDHCP listener is not expected to be active"
		return result
	}
	if strings.ToLower(strings.TrimSpace(h.cfg.BootServiceMode)) == "external" {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICE_MODE=external; DHCP is expected to be provided outside the platform"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	probe, err := services.ProbePXEDHCP(ctx, h.cfg.BootDHCPListenAddr, probeMAC, arch)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.Status = "success"
	detail := fmt.Sprintf("DHCP/ProxyDHCP returned bootfile %s", probe.Bootfile)
	if probe.LegacyBootfile != "" {
		detail += " legacy_bootfile " + probe.LegacyBootfile
	}
	if probe.ServerIP != "" {
		detail += " server_identifier " + probe.ServerIP
	}
	if probe.NextServerIP != "" {
		detail += " next_server " + probe.NextServerIP
	}
	if probe.TFTPServerName != "" {
		detail += " tftp_server_name " + probe.TFTPServerName
	}
	if probe.LeaseIP != "" {
		detail += " lease " + probe.LeaseIP
	}
	result.Message = detail
	return result
}

func (h Handler) runLabPXEBootEventCheck(mac string, serverIDs ...uint) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_boot_event", Hostname: "PXE Boot Event", AssetNo: mac, Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	cutoff := now.Add(-labEvidenceFreshnessWindow)
	var event models.BootEvent
	if err := h.db.Where("LOWER(mac) = ? AND LOWER(source) IN ? AND created_at >= ?", strings.ToLower(mac), []string{"http_ipxe", "pxe_dhcp"}, cutoff).Order("created_at desc, id desc").First(&event).Error; err != nil {
		var latest models.BootEvent
		if latestErr := h.db.Where("LOWER(mac) = ?", strings.ToLower(mac)).Order("created_at desc, id desc").First(&latest).Error; latestErr != nil {
			result.Message = "no PXE boot event recorded for " + mac
			return result
		}
		if labBootEventCountsAsPXEProof(latest.Source) {
			result.Message = fmt.Sprintf("latest physical PXE boot event for %s was recorded at %s; strict PXE validation requires a fresh event within %s", mac, latest.CreatedAt.UTC().Format(time.RFC3339), formatEvidenceWindow(labEvidenceFreshnessWindow))
			return result
		}
		result.Message = fmt.Sprintf("latest boot event for %s was recorded via %s; strict PXE validation requires http_ipxe or pxe_dhcp source", mac, bootEventSourceLabel(latest.Source))
		return result
	}
	matchedServerID, matchesRequestedTarget := h.labPXEBootEventRequestedServerID(event, serverIDs)
	if !matchesRequestedTarget {
		result.Message = fmt.Sprintf("PXE boot event for %s was recorded at %s from %s via %s, but it does not match requested server_ids %s", mac, event.CreatedAt.UTC().Format(time.RFC3339), event.RemoteAddr, bootEventSourceLabel(event.Source), formatUintIDs(serverIDs))
		return result
	}
	result.ServerID = matchedServerID
	if matchedServerID != 0 {
		ref := h.labServerRef(matchedServerID)
		result.Hostname = ref.Hostname
		result.AssetNo = ref.AssetNo
	}
	result.Status = "success"
	result.Message = fmt.Sprintf("PXE boot event for %s recorded at %s from %s via %s", mac, event.CreatedAt.UTC().Format(time.RFC3339), event.RemoteAddr, bootEventSourceLabel(event.Source))
	return result
}

func (h Handler) labPXEBootEventRequestedServerID(event models.BootEvent, serverIDs []uint) (uint, bool) {
	if len(serverIDs) == 0 {
		return valueOrZero(event.ServerID), true
	}
	if event.ServerID != nil {
		return *event.ServerID, uintSliceContains(serverIDs, *event.ServerID)
	}
	var servers []models.Server
	if err := h.db.Select("id", "primary_mac").Where("id IN ?", serverIDs).Find(&servers).Error; err != nil {
		return 0, false
	}
	for _, server := range servers {
		if sameLabSubject(server.PrimaryMAC, event.MAC) {
			return server.ID, true
		}
	}
	return 0, false
}

func formatUintIDs(ids []uint) string {
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func labPreferredPXEBootEvent(candidate models.BootEvent, current models.BootEvent) bool {
	candidateProof := labBootEventCountsAsPXEProof(candidate.Source)
	currentProof := labBootEventCountsAsPXEProof(current.Source)
	if candidateProof != currentProof {
		return candidateProof
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

func labBootEventCountsAsPXEProof(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "http_ipxe", "pxe_dhcp":
		return true
	default:
		return false
	}
}

func bootEventSourceLabel(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "unknown"
	}
	return source
}

func (h Handler) runLabPXETFTPCheck(parent context.Context) labValidationRunResult {
	result := labValidationRunResult{Kind: "pxe_tftp", Hostname: "PXE TFTP", AssetNo: "system", Status: "failed"}
	now := time.Now().UTC()
	result.CheckedAt = &now
	if !h.cfg.BootServicesEnabled {
		result.Status = "skipped"
		result.Message = "BOOT_SERVICES_ENABLED=false; TFTP listener is not expected to be active"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	data, tftpOptions, err := services.ProbeTFTPFileWithOptions(ctx, h.cfg.BootTFTPListenAddr, "boot.ipxe", 64*1024, map[string]string{"blksize": "1024", "timeout": "1", "tsize": "0"})
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if !strings.Contains(string(data), "#!ipxe") {
		result.Message = "TFTP boot.ipxe did not return an iPXE script"
		return result
	}
	if tftpOptions["blksize"] == "" || tftpOptions["tsize"] == "" {
		result.Message = "TFTP boot.ipxe did not negotiate blksize/tsize options"
		return result
	}
	result.Status = "success"
	result.Message = "TFTP boot.ipxe returned a valid iPXE script with OACK blksize=" + tftpOptions["blksize"] + " tsize=" + tftpOptions["tsize"]
	return result
}

func labIPXEProbeURL(baseURL string, mac string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("invalid BOOT_BASE_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("BOOT_BASE_URL must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("BOOT_BASE_URL must include host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/boot/ipxe"
	parsed.RawQuery = url.Values{
		"mac":      []string{mac},
		"arch":     []string{"x86_64"},
		"firmware": []string{"uefi"},
	}.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

type labServerReference struct {
	Hostname string
	AssetNo  string
	Status   string
}

func (h Handler) labServerRef(serverID uint) labServerReference {
	var server models.Server
	if err := h.db.Select("hostname", "asset_no", "status").First(&server, serverID).Error; err != nil {
		return labServerReference{}
	}
	return labServerReference{Hostname: server.Hostname, AssetNo: server.AssetNo, Status: server.Status}
}

func valueOrZero(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}

func (h Handler) runLabBMCChecks(parent context.Context, limit int, serverIDs []uint, requireIdentity bool) []labValidationRunResult {
	var endpoints []models.BmcEndpoint
	query := h.db.Order("updated_at desc, id desc")
	if len(serverIDs) > 0 {
		query = query.Where("server_id IN ?", serverIDs)
	}
	query.Limit(limit).Find(&endpoints)
	results := make([]labValidationRunResult, 0, len(endpoints)+len(serverIDs))
	endpointByServer := map[uint]bool{}
	for _, endpoint := range endpoints {
		endpointByServer[endpoint.ServerID] = true
		ref := h.labServerRef(endpoint.ServerID)
		result := labValidationRunResult{Kind: "bmc", ServerID: endpoint.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "failed"}
		if h.bmc.AdapterName() == "simulated" {
			result.Status = "skipped"
			result.Message = "BMC_ADAPTER=simulated; set redfish or ipmi before running physical validation"
			results = append(results, result)
			continue
		}
		if terminalServerStatus(ref.Status) {
			result.Status = "skipped"
			result.Message = "terminal lifecycle server is skipped"
			results = append(results, result)
			continue
		}
		if issue := h.labBMCEndpointPolicyIssue(endpoint); issue != "" {
			result.Message = issue
			proof := bmcFailureProof(h.bmc.AdapterName(), endpoint, errors.New(issue))
			if labBMCFirmwareHasAnyField(proof) {
				result.Details = labBMCFirmwareDetails(proof)
			}
			results = append(results, result)
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		checked, err := h.bmc.Check(ctx, strconv.FormatUint(uint64(endpoint.ServerID), 10))
		cancel()
		result.CheckedAt = checked.LastCheckedAt
		if err != nil {
			result.Message = err.Error()
			proof := bmcFailureProof(h.bmc.AdapterName(), checked, err)
			if labBMCFirmwareHasAnyField(proof) {
				result.Details = labBMCFirmwareDetails(proof)
			}
			results = append(results, result)
			continue
		}
		identityCtx, identityCancel := context.WithTimeout(parent, 15*time.Second)
		_, info, identityErr := h.bmc.FirmwareInfo(identityCtx, strconv.FormatUint(uint64(endpoint.ServerID), 10))
		identityCancel()
		if identityErr == nil || labBMCFirmwareHasAnyField(info) {
			result.Details = labBMCFirmwareDetails(info)
		}
		if identityErr != nil && requireIdentity {
			result.Message = "BMC connectivity passed but firmware identity probe failed: " + identityErr.Error()
			results = append(results, result)
			continue
		}
		if requireIdentity && !labBMCFirmwareHasIdentity(info) {
			result.Message = "BMC connectivity passed but firmware identity probe returned no manufacturer, model, device/product ID, serial, BIOS, firmware, or BMC version"
			results = append(results, result)
			continue
		}
		result.Status = "success"
		if identityErr != nil {
			result.Message = "BMC connectivity check passed; firmware identity probe unavailable: " + identityErr.Error()
		} else {
			result.Message = "BMC connectivity and identity probe passed: " + labBMCFirmwareSummary(info)
		}
		results = append(results, result)
	}
	for _, serverID := range serverIDs {
		if endpointByServer[serverID] {
			continue
		}
		ref := h.labServerRef(serverID)
		results = append(results, labValidationRunResult{Kind: "bmc", ServerID: serverID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "skipped", Message: "BMC endpoint is not configured for requested server; BMC is optional for this target"})
	}
	return results
}

func labBMCFirmwareHasIdentity(info services.BMCFirmwareInfo) bool {
	return strings.TrimSpace(info.Manufacturer) != "" ||
		strings.TrimSpace(info.ManufacturerID) != "" ||
		strings.TrimSpace(info.Model) != "" ||
		strings.TrimSpace(info.ProductID) != "" ||
		strings.TrimSpace(info.DeviceID) != "" ||
		strings.TrimSpace(info.DeviceRevision) != "" ||
		strings.TrimSpace(info.SerialNumber) != "" ||
		strings.TrimSpace(info.FirmwareVersion) != "" ||
		strings.TrimSpace(info.BIOSVersion) != "" ||
		strings.TrimSpace(info.BMCVersion) != ""
}

func labBMCFirmwareHasAnyField(info services.BMCFirmwareInfo) bool {
	return strings.TrimSpace(info.Adapter) != "" ||
		strings.TrimSpace(info.EndpointStatus) != "" ||
		strings.TrimSpace(info.Stage) != "" ||
		labBMCFirmwareHasIdentity(info) ||
		info.LastCheckedAt != nil
}

func labBMCFirmwareDetails(info services.BMCFirmwareInfo) datatypes.JSON {
	payload, err := json.Marshal(info)
	if err != nil || string(payload) == "{}" || string(payload) == "null" {
		return nil
	}
	return datatypes.JSON(payload)
}

func labBMCFirmwareSummary(info services.BMCFirmwareInfo) string {
	parts := []string{}
	add := func(key string, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, key+"="+truncateLabResultText(value, 120))
		}
	}
	add("adapter", info.Adapter)
	add("manufacturer", info.Manufacturer)
	add("manufacturer_id", info.ManufacturerID)
	add("model", info.Model)
	add("product_id", info.ProductID)
	add("device_id", info.DeviceID)
	add("device_revision", info.DeviceRevision)
	add("serial", info.SerialNumber)
	add("firmware", info.FirmwareVersion)
	add("bios", info.BIOSVersion)
	add("bmc", info.BMCVersion)
	if len(parts) == 0 {
		return "no identity fields returned"
	}
	return strings.Join(parts, "; ")
}

func (h Handler) runLabSSHChecks(parent context.Context, limit int, serverIDs []uint, probeCommand string) []labValidationRunResult {
	probeCommand = strings.TrimSpace(probeCommand)
	if probeCommand == "" {
		probeCommand = services.DefaultSSHCheckCommand
	}
	var accesses []models.SSHAccess
	query := h.db.Order("updated_at desc, id desc")
	if len(serverIDs) > 0 {
		query = query.Where("server_id IN ?", serverIDs)
	}
	query.Limit(limit).Find(&accesses)
	results := make([]labValidationRunResult, 0, len(accesses)+len(serverIDs))
	accessByServer := map[uint]bool{}
	for _, access := range accesses {
		accessByServer[access.ServerID] = true
		ref := h.labServerRef(access.ServerID)
		result := labValidationRunResult{Kind: "ssh", ServerID: access.ServerID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "failed"}
		if terminalServerStatus(ref.Status) {
			result.Status = "skipped"
			result.Message = "terminal lifecycle server is skipped"
			results = append(results, result)
			continue
		}
		if issue := h.labSSHPolicyIssue(); issue != "" {
			result.Message = issue
			results = append(results, result)
			continue
		}
		ctx, cancel := context.WithTimeout(parent, h.cfg.SSHConnectTimeout+5*time.Second)
		checked, remote, err := h.sshExecutor.CheckWithCommand(ctx, access.ServerID, probeCommand)
		cancel()
		result.CheckedAt = checked.LastCheckedAt
		result.Details = labRemoteCommandDetails(probeCommand, remote, err)
		if err != nil {
			result.Message = "SSH probe failed: " + labRemoteCommandSummary(remote, err)
			results = append(results, result)
			continue
		}
		result.Status = "success"
		result.Message = "SSH probe passed: " + labRemoteCommandSummary(remote, nil)
		results = append(results, result)
	}
	for _, serverID := range serverIDs {
		if accessByServer[serverID] {
			continue
		}
		ref := h.labServerRef(serverID)
		results = append(results, labValidationRunResult{Kind: "ssh", ServerID: serverID, Hostname: ref.Hostname, AssetNo: ref.AssetNo, Status: "failed", Message: "SSH access is not configured for requested server"})
	}
	return results
}

func (h Handler) labSSHPolicyIssue() string {
	policy := strings.ToLower(strings.TrimSpace(h.cfg.SSHHostKeyPolicy))
	if policy == "" {
		policy = "insecure_ignore"
	}
	if config.IsProduction(h.cfg.AppEnv) && policy != "known_hosts" {
		return "production SSH validation requires SSH_HOST_KEY_POLICY=known_hosts"
	}
	if policy == "known_hosts" {
		path := strings.TrimSpace(h.cfg.SSHKnownHostsPath)
		if path == "" {
			return "SSH_KNOWN_HOSTS_PATH is required when SSH_HOST_KEY_POLICY=known_hosts"
		}
		info, err := os.Stat(path)
		if err != nil {
			return "SSH_KNOWN_HOSTS_PATH is not readable: " + err.Error()
		}
		if info.IsDir() {
			return "SSH_KNOWN_HOSTS_PATH must point to a known_hosts file"
		}
		if _, err := config.CheckKnownHostsCoverage(path, nil); err != nil {
			return err.Error()
		}
	}
	return ""
}

func labRemoteCommandSummary(result services.RemoteCommandResult, err error) string {
	parts := []string{fmt.Sprintf("exit_code=%d", result.ExitCode)}
	if strings.TrimSpace(result.FailureStage) != "" {
		parts = append(parts, "stage="+truncateLabResultText(result.FailureStage, 80))
	}
	if strings.TrimSpace(result.HostKeyPolicy) != "" {
		parts = append(parts, "host_key_policy="+truncateLabResultText(result.HostKeyPolicy, 80))
	}
	if result.HostKeyVerified {
		parts = append(parts, "host_key_verified=true")
	}
	if strings.TrimSpace(result.HostKeySHA256) != "" {
		parts = append(parts, "host_key_sha256="+truncateLabResultText(result.HostKeySHA256, 120))
	}
	if stdout := strings.TrimSpace(result.Stdout); stdout != "" {
		parts = append(parts, "stdout="+truncateLabResultText(stdout, 300))
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "stderr="+truncateLabResultText(stderr, 300))
	}
	if err != nil {
		parts = append(parts, "error="+truncateLabResultText(err.Error(), 300))
	}
	return strings.Join(parts, "; ")
}

func labRemoteCommandDetails(command string, result services.RemoteCommandResult, err error) datatypes.JSON {
	payload := map[string]any{
		"command":   truncateLabResultText(command, 255),
		"exit_code": result.ExitCode,
	}
	if stage := strings.TrimSpace(result.FailureStage); stage != "" {
		payload["stage"] = truncateLabResultText(stage, 80)
	}
	if policy := strings.TrimSpace(result.HostKeyPolicy); policy != "" {
		payload["host_key_policy"] = truncateLabResultText(policy, 80)
		payload["host_key_verified"] = result.HostKeyVerified
	}
	if algorithm := strings.TrimSpace(result.HostKeyAlgorithm); algorithm != "" {
		payload["host_key_algorithm"] = truncateLabResultText(algorithm, 80)
	}
	if fingerprint := strings.TrimSpace(result.HostKeySHA256); fingerprint != "" {
		payload["host_key_sha256"] = truncateLabResultText(fingerprint, 120)
	}
	if host := strings.TrimSpace(result.HostKeyHost); host != "" {
		payload["host_key_host"] = truncateLabResultText(host, 255)
	}
	if remote := strings.TrimSpace(result.HostKeyRemote); remote != "" {
		payload["host_key_remote"] = truncateLabResultText(remote, 255)
	}
	if stdout := strings.TrimSpace(result.Stdout); stdout != "" {
		payload["stdout"] = truncateLabResultText(stdout, 2000)
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		payload["stderr"] = truncateLabResultText(stderr, 2000)
	}
	if err != nil {
		payload["error"] = truncateLabResultText(err.Error(), 1000)
	}
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return nil
	}
	return datatypes.JSON(data)
}

func truncateLabResultText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
