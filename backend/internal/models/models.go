package models

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type User struct {
	ID           uint           `json:"id" gorm:"primaryKey"`
	Email        string         `json:"email" gorm:"uniqueIndex;size:180;not null"`
	Name         string         `json:"name" gorm:"size:120;not null"`
	Role         string         `json:"role" gorm:"size:40;not null;default:admin"`
	PasswordHash string         `json:"-" gorm:"not null"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

type Server struct {
	ID              uint           `json:"id" gorm:"primaryKey"`
	AssetNo         string         `json:"asset_no" gorm:"size:80;index"`
	Hostname        string         `json:"hostname" gorm:"size:120;index"`
	Status          string         `json:"status" gorm:"size:40;index;not null;default:discovered"`
	Architecture    string         `json:"architecture" gorm:"size:40;default:x86_64"`
	SerialNumber    string         `json:"serial_number" gorm:"size:120;index"`
	MotherboardUUID string         `json:"motherboard_uuid" gorm:"size:120;index"`
	PrimaryMAC      string         `json:"primary_mac" gorm:"size:80;index"`
	PrimaryIP       string         `json:"primary_ip" gorm:"size:80;index"`
	TenantID        string         `json:"tenant_id" gorm:"size:80;index"`
	Owner           string         `json:"owner" gorm:"size:120"`
	Location        string         `json:"location" gorm:"size:120"`
	Rack            string         `json:"rack" gorm:"size:80"`
	RackUnit        string         `json:"rack_unit" gorm:"size:80"`
	Tags            datatypes.JSON `json:"tags"`
	Notes           string         `json:"notes"`
	DiscoveredAt    *time.Time     `json:"discovered_at"`
	DeployedAt      *time.Time     `json:"deployed_at"`
	RetiredAt       *time.Time     `json:"retired_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

type HardwareInventory struct {
	ID             uint           `json:"id" gorm:"primaryKey"`
	ServerID       uint           `json:"server_id" gorm:"index;not null"`
	CPUSummary     string         `json:"cpu_summary"`
	MemorySummary  string         `json:"memory_summary"`
	DiskSummary    string         `json:"disk_summary"`
	NetworkSummary string         `json:"network_summary"`
	GPUSummary     string         `json:"gpu_summary"`
	RAIDSummary    string         `json:"raid_summary"`
	RawPayload     datatypes.JSON `json:"raw_payload"`
	CollectedBy    string         `json:"collected_by" gorm:"size:80"`
	CollectedAt    time.Time      `json:"collected_at"`
	CreatedAt      time.Time      `json:"created_at"`
}

type ServerStatusHistory struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	ServerID   uint      `json:"server_id" gorm:"index;not null"`
	FromStatus string    `json:"from_status" gorm:"size:40"`
	ToStatus   string    `json:"to_status" gorm:"size:40;index;not null"`
	Reason     string    `json:"reason" gorm:"size:160"`
	ActorEmail string    `json:"actor_email" gorm:"size:180;index"`
	CreatedAt  time.Time `json:"created_at"`
}

type RetirementRecord struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	ServerID    uint      `json:"server_id" gorm:"index;not null"`
	FromStatus  string    `json:"from_status" gorm:"size:40"`
	ToStatus    string    `json:"to_status" gorm:"size:40;index;not null;default:retired"`
	Reason      string    `json:"reason" gorm:"size:500;not null"`
	EraseStatus string    `json:"erase_status" gorm:"size:40;index;not null;default:not_required"`
	EraseMethod string    `json:"erase_method" gorm:"size:120"`
	Evidence    string    `json:"evidence" gorm:"size:2000"`
	RequestedBy string    `json:"requested_by" gorm:"size:180;index"`
	RequestedAt time.Time `json:"requested_at" gorm:"index"`
	CreatedAt   time.Time `json:"created_at"`
}

type Tenant struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	TenantID    string         `json:"tenant_id" gorm:"uniqueIndex;size:80;not null"`
	Name        string         `json:"name" gorm:"size:160;not null"`
	Status      string         `json:"status" gorm:"size:40;index;default:active"`
	Owner       string         `json:"owner" gorm:"size:120"`
	Description string         `json:"description"`
	Quota       datatypes.JSON `json:"quota"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type NetworkConfig struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"size:160;not null;index"`
	Purpose     string         `json:"purpose" gorm:"size:40;index;not null;default:deployment"`
	CIDR        string         `json:"cidr" gorm:"size:80;not null"`
	Gateway     string         `json:"gateway" gorm:"size:80"`
	DNS         string         `json:"dns" gorm:"size:255"`
	VLANID      int            `json:"vlan_id"`
	DHCPMode    string         `json:"dhcp_mode" gorm:"size:40;default:proxy"`
	ProxyDHCP   bool           `json:"proxy_dhcp" gorm:"default:true"`
	Status      string         `json:"status" gorm:"size:40;index;default:enabled"`
	Description string         `json:"description"`
	Options     datatypes.JSON `json:"options"`
	CreatedBy   string         `json:"created_by" gorm:"size:120"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type BmcEndpoint struct {
	ID                   uint       `json:"id" gorm:"primaryKey"`
	ServerID             uint       `json:"server_id" gorm:"uniqueIndex;not null"`
	Type                 string     `json:"type" gorm:"size:40;not null;default:redfish"`
	Endpoint             string     `json:"endpoint" gorm:"size:255;not null"`
	Username             string     `json:"username" gorm:"size:120;not null"`
	EncryptedPasswordRef string     `json:"encrypted_password_ref" gorm:"size:255"`
	Protocol             string     `json:"protocol" gorm:"size:40;default:https"`
	Status               string     `json:"status" gorm:"size:40;default:unknown"`
	PowerState           string     `json:"power_state" gorm:"size:40;default:off"`
	LastCheckedAt        *time.Time `json:"last_checked_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type Credential struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Name       string    `json:"name" gorm:"size:160;not null"`
	Kind       string    `json:"kind" gorm:"size:40;index;not null"`
	Username   string    `json:"username" gorm:"size:120"`
	Ciphertext string    `json:"-" gorm:"not null"`
	CreatedBy  string    `json:"created_by" gorm:"size:120"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type SSHAccess struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	ServerID      uint       `json:"server_id" gorm:"uniqueIndex;not null"`
	Host          string     `json:"host" gorm:"size:160;not null"`
	Port          int        `json:"port" gorm:"default:22"`
	Username      string     `json:"username" gorm:"size:120;not null"`
	AuthType      string     `json:"auth_type" gorm:"size:40;not null;default:password"`
	CredentialRef string     `json:"credential_ref" gorm:"size:120"`
	Status        string     `json:"status" gorm:"size:40;default:unknown"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Image struct {
	ID           uint           `json:"id" gorm:"primaryKey"`
	Name         string         `json:"name" gorm:"size:160;not null;index"`
	OSFamily     string         `json:"os_family" gorm:"size:80;index"`
	OSVersion    string         `json:"os_version" gorm:"size:80"`
	Architecture string         `json:"architecture" gorm:"size:40;default:x86_64"`
	FilePath     string         `json:"file_path" gorm:"size:500"`
	SizeBytes    int64          `json:"size_bytes"`
	SHA256       string         `json:"sha256" gorm:"size:128"`
	Status       string         `json:"status" gorm:"size:40;default:enabled"`
	TestStatus   string         `json:"test_status" gorm:"size:40;default:untested"`
	Tags         datatypes.JSON `json:"tags"`
	CreatedBy    string         `json:"created_by" gorm:"size:120"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

type InstallTemplate struct {
	ID              uint           `json:"id" gorm:"primaryKey"`
	Name            string         `json:"name" gorm:"size:160;not null"`
	OSFamily        string         `json:"os_family" gorm:"size:80;index"`
	OSVersion       string         `json:"os_version" gorm:"size:80"`
	TemplateType    string         `json:"template_type" gorm:"size:80"`
	Content         string         `json:"content"`
	VariablesSchema datatypes.JSON `json:"variables_schema"`
	Version         string         `json:"version" gorm:"size:40;default:v1"`
	Status          string         `json:"status" gorm:"size:40;default:enabled"`
	CreatedBy       string         `json:"created_by" gorm:"size:120"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type WorkflowTemplate struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"size:160;not null"`
	Version     string         `json:"version" gorm:"size:40;default:v1"`
	Description string         `json:"description"`
	Definition  datatypes.JSON `json:"definition"`
	Status      string         `json:"status" gorm:"size:40;default:enabled"`
	CreatedBy   string         `json:"created_by" gorm:"size:120"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Deployment struct {
	ID               uint           `json:"id" gorm:"primaryKey"`
	ServerID         uint           `json:"server_id" gorm:"index;not null"`
	ImageID          uint           `json:"image_id" gorm:"index;not null"`
	TemplateID       *uint          `json:"template_id"`
	WorkflowID       *uint          `json:"workflow_id"`
	NetworkID        *uint          `json:"network_id" gorm:"index"`
	Variables        datatypes.JSON `json:"variables"`
	ErasePolicy      string         `json:"erase_policy" gorm:"size:40;not null;default:quick"`
	EraseConfirmed   bool           `json:"erase_confirmed" gorm:"not null;default:false"`
	EraseConfirmedAt *time.Time     `json:"erase_confirmed_at"`
	Status           string         `json:"status" gorm:"size:40;index;default:pending"`
	RequestedBy      string         `json:"requested_by" gorm:"size:120"`
	StartedAt        *time.Time     `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
	ErrorMessage     string         `json:"error_message"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type BootEvent struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	MAC          string    `json:"mac" gorm:"size:80;index;not null"`
	Architecture string    `json:"architecture" gorm:"size:40;index"`
	Firmware     string    `json:"firmware" gorm:"size:40;index"`
	RemoteAddr   string    `json:"remote_addr" gorm:"size:120"`
	ServerID     *uint     `json:"server_id" gorm:"index"`
	DeploymentID *uint     `json:"deployment_id" gorm:"index"`
	Script       string    `json:"script"`
	CreatedAt    time.Time `json:"created_at"`
}

type MetadataToken struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	Token        string     `json:"token" gorm:"uniqueIndex;size:120;not null"`
	ServerID     uint       `json:"server_id" gorm:"index;not null"`
	DeploymentID *uint      `json:"deployment_id" gorm:"index"`
	ExpiresAt    *time.Time `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type WorkflowRun struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	DeploymentID uint       `json:"deployment_id" gorm:"index;not null"`
	Name         string     `json:"name" gorm:"size:160;not null"`
	Version      string     `json:"version" gorm:"size:40;default:v1"`
	Status       string     `json:"status" gorm:"size:40;index;default:pending"`
	Definition   string     `json:"definition"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type TaskExecution struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	WorkflowRunID uint       `json:"workflow_run_id" gorm:"index;not null"`
	StepName      string     `json:"step_name" gorm:"size:160;not null"`
	Action        string     `json:"action" gorm:"size:120;not null"`
	Status        string     `json:"status" gorm:"size:40;index;default:pending"`
	RetryCount    int        `json:"retry_count"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	Stdout        string     `json:"stdout"`
	Stderr        string     `json:"stderr"`
	ErrorMessage  string     `json:"error_message"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type MetricSample struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	ServerID    uint           `json:"server_id" gorm:"index;not null"`
	MetricName  string         `json:"metric_name" gorm:"size:120;index;not null"`
	Value       float64        `json:"value"`
	Unit        string         `json:"unit" gorm:"size:40"`
	Labels      datatypes.JSON `json:"labels"`
	CollectedAt time.Time      `json:"collected_at" gorm:"index"`
	CreatedAt   time.Time      `json:"created_at"`
}

type LogEvent struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	ServerID   uint      `json:"server_id" gorm:"index"`
	Source     string    `json:"source" gorm:"size:80;index;not null"`
	Level      string    `json:"level" gorm:"size:40;index;not null;default:info"`
	Message    string    `json:"message" gorm:"not null"`
	TraceID    string    `json:"trace_id" gorm:"size:120;index"`
	OccurredAt time.Time `json:"occurred_at" gorm:"index"`
	CreatedAt  time.Time `json:"created_at"`
}

type CollectionJob struct {
	ID           uint       `json:"id" gorm:"primaryKey"`
	ServerID     uint       `json:"server_id" gorm:"index;not null"`
	Mode         string     `json:"mode" gorm:"size:40;not null;default:ssh_agentless"`
	Status       string     `json:"status" gorm:"size:40;index;default:pending"`
	RequestedBy  string     `json:"requested_by" gorm:"size:120"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ErrorMessage string     `json:"error_message"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type ScriptJob struct {
	ID             uint           `json:"id" gorm:"primaryKey"`
	Name           string         `json:"name" gorm:"size:160;not null"`
	Script         string         `json:"script"`
	ServerIDs      datatypes.JSON `json:"server_ids"`
	Status         string         `json:"status" gorm:"size:40;index;default:pending"`
	RequestedBy    string         `json:"requested_by" gorm:"size:120"`
	Concurrency    int            `json:"concurrency" gorm:"default:5"`
	TimeoutSeconds int            `json:"timeout_seconds" gorm:"default:60"`
	StartedAt      *time.Time     `json:"started_at"`
	FinishedAt     *time.Time     `json:"finished_at"`
	ErrorMessage   string         `json:"error_message"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type ScriptExecution struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	ScriptJobID uint       `json:"script_job_id" gorm:"index;not null"`
	ServerID    uint       `json:"server_id" gorm:"index;not null"`
	Status      string     `json:"status" gorm:"size:40;index;default:pending"`
	ExitCode    int        `json:"exit_code"`
	Stdout      string     `json:"stdout"`
	Stderr      string     `json:"stderr"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type TerminalSession struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	ServerID    uint       `json:"server_id" gorm:"index;not null"`
	Status      string     `json:"status" gorm:"size:40;index;default:active"`
	Mode        string     `json:"mode" gorm:"size:40;not null;default:simulated"`
	RequestedBy string     `json:"requested_by" gorm:"size:120"`
	Reason      string     `json:"reason" gorm:"size:255"`
	Transcript  string     `json:"transcript"`
	OpenedAt    time.Time  `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Alert struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	ServerID       uint       `json:"server_id" gorm:"index"`
	RuleID         string     `json:"rule_id" gorm:"size:120;index"`
	Severity       string     `json:"severity" gorm:"size:40;index;default:warning"`
	Status         string     `json:"status" gorm:"size:40;index;default:firing"`
	Title          string     `json:"title" gorm:"size:255;not null"`
	Description    string     `json:"description"`
	TriggeredAt    time.Time  `json:"triggered_at"`
	AcknowledgedBy string     `json:"acknowledged_by" gorm:"size:180;index"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	ResolvedBy     string     `json:"resolved_by" gorm:"size:180;index"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AlertRule struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	RuleID      string    `json:"rule_id" gorm:"uniqueIndex;size:120;not null"`
	Name        string    `json:"name" gorm:"size:160;not null"`
	Description string    `json:"description"`
	MetricName  string    `json:"metric_name" gorm:"size:120;index;not null"`
	Operator    string    `json:"operator" gorm:"size:16;not null;default:>"`
	Threshold   float64   `json:"threshold"`
	Severity    string    `json:"severity" gorm:"size:40;index;default:warning"`
	Status      string    `json:"status" gorm:"size:40;index;default:enabled"`
	CreatedBy   string    `json:"created_by" gorm:"size:120"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AlertEvent struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	AlertID    uint      `json:"alert_id" gorm:"index;not null"`
	Action     string    `json:"action" gorm:"size:40;index;not null"`
	ActorID    uint      `json:"actor_id" gorm:"index"`
	ActorEmail string    `json:"actor_email" gorm:"size:180;index"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

type AuditLog struct {
	ID             uint           `json:"id" gorm:"primaryKey"`
	ActorID        uint           `json:"actor_id" gorm:"index"`
	ActorEmail     string         `json:"actor_email" gorm:"size:180;index"`
	TenantID       string         `json:"tenant_id" gorm:"size:80;index"`
	Action         string         `json:"action" gorm:"size:120;index;not null"`
	ResourceType   string         `json:"resource_type" gorm:"size:120;index"`
	ResourceID     string         `json:"resource_id" gorm:"size:120;index"`
	RiskLevel      string         `json:"risk_level" gorm:"size:40;default:low"`
	RequestID      string         `json:"request_id" gorm:"size:120;index"`
	ClientIP       string         `json:"client_ip" gorm:"size:120"`
	UserAgent      string         `json:"user_agent"`
	BeforeSnapshot datatypes.JSON `json:"before_snapshot"`
	AfterSnapshot  datatypes.JSON `json:"after_snapshot"`
	CreatedAt      time.Time      `json:"created_at"`
}

func All() []any {
	return []any{
		&User{}, &Server{}, &HardwareInventory{}, &ServerStatusHistory{}, &RetirementRecord{}, &Tenant{}, &NetworkConfig{}, &BmcEndpoint{}, &Credential{}, &SSHAccess{}, &Image{}, &InstallTemplate{}, &WorkflowTemplate{},
		&Deployment{}, &BootEvent{}, &MetadataToken{}, &WorkflowRun{}, &TaskExecution{}, &MetricSample{}, &LogEvent{}, &CollectionJob{}, &ScriptJob{}, &ScriptExecution{}, &TerminalSession{}, &Alert{}, &AlertRule{}, &AlertEvent{}, &AuditLog{},
	}
}
