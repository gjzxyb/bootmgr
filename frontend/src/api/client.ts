import axios, { AxiosHeaders, type AxiosInstance } from 'axios';

declare module 'axios' {
  export interface AxiosRequestConfig {
    suppressGlobalError?: boolean;
  }
}

export const api = axios.create({ baseURL: apiBaseURL() });
export const rootApi = axios.create({ baseURL: apiRootURL() });

export function setToken(token: string | null) {
  for (const client of [api, rootApi]) {
    if (token) client.defaults.headers.common.Authorization = `Bearer ${token}`;
    else delete client.defaults.headers.common.Authorization;
  }
}

export function apiErrorMessage(error: unknown, fallback: string) {
  if (!axios.isAxiosError(error)) return fallback;
  if (!error.response) return '无法连接后端服务，请检查 API 是否已启动';
  return responseErrorDetail(error.response.data, error.response.headers, fallback);
}

const token = localStorage.getItem('token');
if (token) setToken(token);

installInterceptors(api);
installInterceptors(rootApi);

function installInterceptors(client: AxiosInstance) {
  client.interceptors.request.use(config => {
    const headers = AxiosHeaders.from(config.headers);
    if (!headers.has('X-Request-ID')) headers.set('X-Request-ID', newClientRequestID());
    config.headers = headers;
    return config;
  });

  client.interceptors.response.use(
    response => response,
    error => {
      if (!axios.isAxiosError(error)) return Promise.reject(error);
      const status = error.response?.status;
      const suppressGlobalError = Boolean(error.config?.suppressGlobalError);
      if (error?.response?.status === 401) {
        localStorage.removeItem('token');
        setToken(null);
        if (window.location.pathname !== '/') window.location.assign('/');
      } else if (error?.response?.status === 403) {
        window.dispatchEvent(new CustomEvent('api:forbidden'));
      } else if (!error.response) {
        if (suppressGlobalError) return Promise.reject(error);
        window.dispatchEvent(new CustomEvent('api:error', { detail: '无法连接后端服务，请检查 API 是否已启动' }));
      } else if (!suppressGlobalError && (status === 400 || status === 409 || status === 413 || status === 428)) {
        window.dispatchEvent(new CustomEvent('api:error', { detail: responseErrorDetail(error.response.data, error.response.headers, '请求未通过校验') }));
      } else if (error.response.status >= 500) {
        window.dispatchEvent(new CustomEvent('api:error', { detail: responseErrorDetail(error.response.data, error.response.headers, '后端服务异常，请稍后重试') }));
      }
      return Promise.reject(error);
    }
  );
}

function newClientRequestID() {
  if (window.crypto?.randomUUID) return window.crypto.randomUUID();
  return `web-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function apiBaseURL() {
  const configured = configuredURL(import.meta.env.VITE_API_BASE_URL);
  if (configured) return configured;
  const currentOrigin = new URL(window.location.origin);
  currentOrigin.port = configuredAPIPort();
  return `${currentOrigin.origin}/api/v1`;
}

function apiRootURL() {
  const configured = configuredURL(import.meta.env.VITE_API_ROOT_URL);
  if (configured) return configured;
  const apiBase = apiBaseURL();
  const parsed = new URL(apiBase, window.location.origin);
  parsed.pathname = parsed.pathname.replace(/\/api\/v1\/?$/, '');
  parsed.search = '';
  parsed.hash = '';
  const path = parsed.pathname === '/' ? '' : parsed.pathname.replace(/\/$/, '');
  return `${parsed.origin}${path}`;
}

function configuredURL(value: unknown) {
  return typeof value === 'string' && value.trim() ? value.trim() : '';
}

function configuredAPIPort() {
  const configured = import.meta.env.VITE_API_PORT;
  return typeof configured === 'string' && configured.trim() ? configured.trim() : '8080';
}

function responseHeader(headers: unknown, name: string) {
  if (headers instanceof AxiosHeaders) {
    const value = headers.get(name);
    return typeof value === 'string' ? value : '';
  }
  if (!headers || typeof headers !== 'object') return '';
  const values = headers as Record<string, unknown>;
  const value = values[name] ?? values[name.toLowerCase()];
  return typeof value === 'string' ? value : '';
}

function responseErrorDetail(data: unknown, headers: unknown, fallback: string) {
  const requestID = responseHeader(headers, 'X-Request-ID');
  const suffix = requestID ? ` (Request ID: ${requestID})` : '';
  if (data && typeof data === 'object') {
    const body = data as { error?: unknown; problems?: unknown };
    if (typeof body.error === 'string') {
      const problems = Array.isArray(body.problems) ? body.problems.filter(item => typeof item === 'string') : [];
      return problems.length ? `${body.error}: ${problems.join('; ')}${suffix}` : `${body.error}${suffix}`;
    }
  }
  return `${fallback}${suffix}`;
}

export type User = { id: number; email: string; name?: string; role: string; created_at?: string; updated_at?: string };
export type Tenant = { id: number; tenant_id: string; name: string; status: string; owner: string; description: string; quota?: unknown; created_at: string; updated_at: string };
export type NetworkConfig = { id: number; name: string; purpose: string; cidr: string; gateway?: string; dns?: string; vlan_id?: number; dhcp_mode: string; proxy_dhcp: boolean; status: string; description?: string; options?: unknown; created_by: string; created_at: string; updated_at: string };
export type Server = { id: number; asset_no: string; hostname: string; status: string; architecture: string; serial_number?: string; motherboard_uuid?: string; primary_ip: string; primary_mac: string; tenant_id?: string; owner: string; location: string; rack: string; rack_unit: string; tags?: unknown; notes?: string };
export type RetirementRecord = { id: number; server_id: number; from_status: string; to_status: string; reason: string; erase_status: 'not_required' | 'pending' | 'verified' | 'failed'; erase_method: string; evidence: string; requested_by: string; requested_at: string; created_at: string };
export type Image = { id: number; name: string; os_family: string; os_version: string; architecture: string; status: string; test_status: string; sha256: string; file_path: string; size_bytes?: number; tags?: unknown };
export type Deployment = { id: number; server_id: number; image_id: number; template_id?: number | null; workflow_id?: number | null; network_id?: number | null; erase_policy: 'none' | 'quick' | 'full' | 'external_verified'; erase_confirmed: boolean; erase_confirmed_at?: string; status: string; requested_by: string; created_at: string; started_at?: string; finished_at?: string; error_message?: string };
export type Alert = { id: number; server_id: number; severity: string; status: string; title: string; description: string; triggered_at: string; acknowledged_by?: string; acknowledged_at?: string; resolved_by?: string; resolved_at?: string };
export type AlertRule = { id: number; rule_id: string; name: string; description: string; metric_name: string; operator: string; threshold: number; severity: string; status: string; created_by: string; created_at: string };
export type AlertEvent = { id: number; alert_id: number; action: string; actor_email: string; note: string; created_at: string };
export type MetricSample = { id: number; server_id: number; metric_name: string; value: number; unit: string; collected_at: string; created_at: string };
export type LogEvent = { id: number; server_id: number; source: string; level: string; message: string; trace_id: string; occurred_at: string; created_at: string };
export type CollectionJob = { id: number; server_id: number; mode: string; status: string; requested_by: string; started_at?: string; finished_at?: string; error_message?: string; created_at: string };
export type SSHAccess = { id: number; server_id: number; host: string; port: number; username: string; auth_type: string; status: string; last_checked_at?: string; created_at: string; updated_at: string };
export type SSHCheckProof = { command: string; exit_code: number; stage?: string; stdout?: string; stderr?: string; host_key_policy?: string; host_key_verified?: boolean; host_key_algorithm?: string; host_key_sha256?: string; host_key_host?: string; host_key_remote?: string };
export type SSHCheckResponse = { status: string; checked_at?: string; proof?: SSHCheckProof; error?: string; detail?: string };
export type BMCFirmwareInfo = { adapter: string; endpoint_status: string; stage?: string; manufacturer?: string; manufacturer_id?: string; model?: string; product_id?: string; device_id?: string; device_revision?: string; serial_number?: string; firmware_version?: string; bios_version?: string; bmc_version?: string; last_checked_at?: string };
export type BMCCheckResponse = { status: string; checked_at?: string; proof?: BMCFirmwareInfo; proof_error?: string; error?: string; detail?: string };
export type AuditLog = { id: number; actor_email: string; action: string; resource_type: string; resource_id: string; risk_level: string; client_ip: string; created_at: string };
export type PageResult<T> = { items: T[]; total: number; page: number; page_size: number };
export type NetworkCheck = { name: string; status: 'ok' | 'warning' | 'error'; message: string };
export type NetworkCheckReport = { status: 'ok' | 'warning' | 'error'; checks: NetworkCheck[] };
export type InstallTemplate = { id: number; name: string; os_family: string; os_version: string; template_type: string; content?: string; variables_schema?: unknown; version: string; status: string };
export type WorkflowTemplate = { id: number; name: string; version: string; description?: string; definition?: unknown; status: string };
export type ScriptJob = { id: number; name: string; status: string; requested_by: string; concurrency: number; timeout_seconds: number; created_at: string; started_at?: string; finished_at?: string };
export type ScriptExecution = { id: number; script_job_id: number; server_id: number; status: string; exit_code: number; stdout: string; stderr: string; started_at?: string; finished_at?: string };
export type TerminalSession = { id: number; server_id: number; status: string; mode: string; requested_by: string; reason: string; transcript: string; opened_at: string; closed_at?: string; created_at: string };
export type ReadinessCheck = { name: string; status: 'ok' | 'warning' | 'error'; message: string };
export type ConfigIssue = { level: 'error' | 'warning'; key: string; message: string };
export type ReadinessStatus = { status: 'ok' | 'degraded'; checks: ReadinessCheck[]; config_issues: ConfigIssue[] };
export type LabValidationCheck = { name: string; status: 'ok' | 'warning' | 'error'; message: string };
export type LabBootEvent = { id: number; mac: string; architecture: string; firmware: string; remote_addr: string; source: string; server_id?: number; deployment_id?: number; created_at: string };
export type LabBMCRef = { server_id: number; hostname: string; asset_no: string; type: string; protocol: string; endpoint: string; status: string; power_state: string; last_checked_at?: string; updated_at: string };
export type LabSSHRef = { server_id: number; hostname: string; asset_no: string; host: string; port: number; username: string; auth_type: string; status: string; last_checked_at?: string; updated_at: string };
export type LabValidationRunResult = { id?: number; run_id?: number; kind: 'strict_physical_targets' | 'full_chain_target' | 'pxe_http' | 'pxe_dhcp' | 'pxe_tftp' | 'pxe_boot_event' | 'bmc' | 'ssh'; server_id: number; hostname: string; asset_no: string; status: 'success' | 'failed' | 'skipped'; message: string; details?: Record<string, unknown>; checked_at?: string };
export type LabValidationRunSummary = { id: number; status: 'ok' | 'warning' | 'error' | 'running'; strict: boolean; check_pxe: boolean; check_bmc: boolean; check_ssh: boolean; limit: number; server_ids: number[]; pxe_macs: string[]; pxe_probe_mac?: string; pxe_arch: number; ssh_probe_command?: string; requested_by: string; request_id: string; started_at: string; finished_at?: string; results: number; failures: number; skipped: number };
export type LabValidationRunDetail = { run: LabValidationRunSummary; results: LabValidationRunResult[] };
export type LabValidationEvidence = { id: number; kind: 'pxe' | 'bmc' | 'ssh' | 'full'; subject: string; status: 'ok' | 'warning' | 'error'; summary: string; details?: string; artifact_url?: string; run_id?: number; server_id?: number; boot_event_id?: number; bmc_endpoint_id?: number; ssh_access_id?: number; created_by: string; created_at: string };
export type LabEvidenceCandidate = { kind: 'pxe' | 'bmc' | 'ssh' | 'full'; status: 'ok'; subject: string; summary: string; details?: string; run_id: number; server_id?: number; boot_event_id?: number; source_step: string };
export type LabValidationTarget = { server_id: number; hostname: string; asset_no: string; primary_mac: string; server_status: string; pxe_status: string; pxe_boot_event_id?: number; pxe_boot_at?: string; bmc_required: boolean; bmc_status: string; bmc_checked_at?: string; ssh_status: string; ssh_checked_at?: string; evidence_status: string; evidence_id?: number; evidence_kinds: string[]; latest_run_id?: number; latest_run_status?: 'ok' | 'warning' | 'error' | 'running'; latest_run_strict?: boolean; latest_run_kind?: string; latest_run_result_status?: 'success' | 'failed' | 'skipped'; latest_run_at?: string; full_chain_ready: boolean; blocking_reasons: string[] };
export type LabValidationTerminalSession = { id: number; server_id: number; hostname: string; asset_no: string; status: string; mode: string; requested_by: string; reason: string; transcript: string; opened_at: string; closed_at?: string; created_at: string };
export type LabValidationScriptExecution = { id: number; script_job_id: number; server_id: number; hostname: string; asset_no: string; job_name: string; status: string; exit_code: number; stdout: string; stderr: string; started_at?: string; finished_at?: string; created_at: string };
export type LabValidationLogEvent = { id: number; server_id: number; hostname: string; asset_no: string; source: string; level: string; message: string; trace_id: string; occurred_at: string; created_at: string };
export type LabOperatorChecklistItem = { subject: string; step: string; status: 'ok' | 'warning' | 'pending' | 'error' | 'skipped'; message: string; next_action?: string; run_id: number; server_id?: number; boot_event_id?: number; evidence_id?: number; blocking_reasons?: string[] };
export type LabValidationEvidenceBundle = { generated_at: string; run: LabValidationRunSummary; environment: { app_env: string; boot_base_url: string; bmc_adapter: string; collector_mode: string; ssh_operations_mode: string }; checks: LabValidationCheck[]; results: LabValidationRunResult[]; boot_events: LabBootEvent[]; bmc_endpoints: LabBMCRef[]; ssh_accesses: LabSSHRef[]; terminal_sessions: LabValidationTerminalSession[]; script_executions: LabValidationScriptExecution[]; log_events: LabValidationLogEvent[]; recent_evidence: LabValidationEvidence[]; evidence_candidates: LabEvidenceCandidate[]; targets: LabValidationTarget[]; config_issues: ConfigIssue[]; pxe_runtime_issues: ConfigIssue[]; notes: string[]; operator_checklist: LabOperatorChecklistItem[] };
export type LabValidationReport = {
  status: 'ok' | 'warning' | 'error';
  run_id?: number;
  generated_at: string;
  environment: { app_env: string; boot_base_url: string; bmc_adapter: string; collector_mode: string; ssh_operations_mode: string };
  checks: LabValidationCheck[];
  pxe: {
    enabled: boolean;
    mode: string;
    bind_interface: string;
    dhcp_listen_addr: string;
    dhcp_server_ip: string;
    dhcp_lease_start: string;
    dhcp_lease_end: string;
    tftp_listen_addr: string;
    tftp_root: string;
    bootfile_uefi: string;
    bootfile_bios: string;
    deployment_networks: number;
    boot_events: number;
    recent_boot_events: LabBootEvent[];
    runtime_issues: ConfigIssue[];
  };
  bmc: { adapter: string; total: number; ok: number; error: number; unknown: number; last_checked_at?: string; recent_endpoints: LabBMCRef[] };
  ssh: { collector_mode: string; operations_mode: string; total: number; ok: number; error: number; configured: number; unknown: number; last_checked_at?: string; recent_ssh_accesses: LabSSHRef[] };
  recent_evidence: LabValidationEvidence[];
  targets: LabValidationTarget[];
  recent_runs: LabValidationRunSummary[];
  run_results?: LabValidationRunResult[];
};
export type BackupValidationCheck = { name: string; status: 'ok' | 'warning' | 'error'; message: string };
export type BackupValidationReport = { status: 'ok' | 'warning' | 'error'; version: string; generated_at?: string; totals: Record<string, number>; target_counts: Record<string, number>; checks: BackupValidationCheck[] };
export type BackupRestoreResult = { status: 'restored'; imported: Record<string, number>; warnings?: BackupValidationCheck[] };
