import { DownloadOutlined, EditOutlined, ExperimentOutlined, FileDoneOutlined, LockOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined, SafetyCertificateOutlined, SearchOutlined } from '@ant-design/icons';
import { Button, Descriptions, Form, Input, InputNumber, Modal, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, ConfigIssue, LabBMCRef, LabBootEvent, LabEvidenceCandidate, LabOperatorChecklistItem, LabSSHRef, LabValidationCheck, LabValidationEvidence, LabValidationEvidenceBundle, LabValidationLogEvent, LabValidationReport, LabValidationRunDetail, LabValidationRunResult, LabValidationRunSummary, LabValidationScriptExecution, LabValidationTarget, LabValidationTerminalSession, NetworkCheck, NetworkCheckReport, NetworkConfig, PageResult, ReadinessCheck, ReadinessStatus, rootApi, Tenant, User } from '../api/client';

const roleColor = (role: string) => role === 'admin' ? 'red' : role === 'operator' ? 'blue' : 'default';
const readinessColor = (status: string) => status === 'ok' ? 'green' : status === 'warning' ? 'orange' : 'red';
const labRunColor = (status: string) => status === 'running' ? 'blue' : readinessColor(status);
const runResultColor = (status: string) => status === 'success' ? 'green' : status === 'skipped' ? 'default' : 'red';
const labTargetStatusColor = (status: string) => status === 'ok' ? 'green' : status === 'missing' ? 'default' : status === 'stale' || status === 'partial' || status === 'configured' || status === 'unknown' ? 'orange' : 'red';
const bmcTargetCell = (row: LabValidationTarget) => (
  <Space>
    <Tag color={row.bmc_required ? labTargetStatusColor(row.bmc_status) : 'blue'}>{row.bmc_status}</Tag>
    <Tag color={row.bmc_required ? 'orange' : 'default'}>{row.bmc_required ? 'required' : 'optional'}</Tag>
  </Space>
);
const serverLabel = (row: { hostname?: string; asset_no?: string; server_id: number }) => row.hostname || row.asset_no || `server-${row.server_id}`;
const labRunTargets = (row: LabValidationRunSummary) => {
  const parts = [];
  if (row.server_ids?.length) parts.push(`资产 ${row.server_ids.join(',')}`);
  if (row.pxe_macs?.length) parts.push(`PXE ${row.pxe_macs.join(',')}`);
  return parts.join(' / ') || '-';
};
const runResultDetails = (details?: Record<string, unknown>) => {
  if (!details || !Object.keys(details).length) return '-';
  return Object.entries(details)
    .filter(([, value]) => value !== undefined && value !== null && String(value).trim() !== '')
    .map(([key, value]) => `${key}=${String(value)}`)
    .join('; ')
    .slice(0, 160) || '-';
};
const quotaToText = (quota: unknown) => {
  if (quota === undefined || quota === null || quota === '') return '';
  try {
    return JSON.stringify(quota, null, 2);
  } catch {
    return String(quota);
  }
};

export function SystemPage() {
  const [users, setUsers] = useState<User[]>([]);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [networks, setNetworks] = useState<NetworkConfig[]>([]);
  const [readiness, setReadiness] = useState<ReadinessStatus | null>(null);
  const [readinessLoading, setReadinessLoading] = useState(false);
  const [labValidation, setLabValidation] = useState<LabValidationReport | null>(null);
  const [labValidationLoading, setLabValidationLoading] = useState(false);
  const [labValidationRunLoading, setLabValidationRunLoading] = useState(false);
  const [labRunDetail, setLabRunDetail] = useState<LabValidationRunDetail | null>(null);
  const [labRunDetailOpen, setLabRunDetailOpen] = useState(false);
  const [labRunDetailLoading, setLabRunDetailLoading] = useState(false);
  const [labEvidenceBundle, setLabEvidenceBundle] = useState<LabValidationEvidenceBundle | null>(null);
  const [labEvidenceBundleOpen, setLabEvidenceBundleOpen] = useState(false);
  const [labEvidenceBundleLoading, setLabEvidenceBundleLoading] = useState(false);
  const [labRunOpen, setLabRunOpen] = useState(false);
  const [evidenceOpen, setEvidenceOpen] = useState(false);
  const [evidenceLoading, setEvidenceLoading] = useState(false);
  const [total, setTotal] = useState(0);
  const [tenantTotal, setTenantTotal] = useState(0);
  const [networkTotal, setNetworkTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [tenantPage, setTenantPage] = useState(1);
  const [networkPage, setNetworkPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [tenantPageSize, setTenantPageSize] = useState(20);
  const [networkPageSize, setNetworkPageSize] = useState(20);
  const [createOpen, setCreateOpen] = useState(false);
  const [userOpen, setUserOpen] = useState(false);
  const [tenantOpen, setTenantOpen] = useState(false);
  const [networkOpen, setNetworkOpen] = useState(false);
  const [networkCheckOpen, setNetworkCheckOpen] = useState(false);
  const [networkCheckLoading, setNetworkCheckLoading] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [activeUser, setActiveUser] = useState<User | null>(null);
  const [editingUser, setEditingUser] = useState<User | null>(null);
  const [editingTenant, setEditingTenant] = useState<Tenant | null>(null);
  const [editingNetwork, setEditingNetwork] = useState<NetworkConfig | null>(null);
  const [checkingNetwork, setCheckingNetwork] = useState<NetworkConfig | null>(null);
  const [networkCheck, setNetworkCheck] = useState<NetworkCheckReport | null>(null);
  const [filterForm] = Form.useForm();
  const [tenantFilterForm] = Form.useForm();
  const [networkFilterForm] = Form.useForm();
  const [createForm] = Form.useForm();
  const [userForm] = Form.useForm();
  const [tenantForm] = Form.useForm();
  const [networkForm] = Form.useForm();
  const [passwordForm] = Form.useForm();
  const [labRunForm] = Form.useForm();
  const [evidenceForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const loadUsers = async (nextPage = page, nextPageSize = pageSize) => {
    const values = filterForm.getFieldsValue();
    const { data } = await api.get<PageResult<User>>('/users', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setUsers(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };

  const loadTenants = async (nextPage = tenantPage, nextPageSize = tenantPageSize) => {
    const values = tenantFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<Tenant>>('/tenants', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setTenants(data.items);
    setTenantTotal(data.total);
    setTenantPage(data.page);
    setTenantPageSize(data.page_size);
  };

  const loadNetworks = async (nextPage = networkPage, nextPageSize = networkPageSize) => {
    const values = networkFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<NetworkConfig>>('/network-configs', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setNetworks(data.items);
    setNetworkTotal(data.total);
    setNetworkPage(data.page);
    setNetworkPageSize(data.page_size);
  };

  const loadReadiness = async () => {
    setReadinessLoading(true);
    try {
      const { data } = await rootApi.get<ReadinessStatus>('/readyz');
      setReadiness(data);
    } finally {
      setReadinessLoading(false);
    }
  };

  const loadLabValidation = async () => {
    setLabValidationLoading(true);
    try {
      const { data } = await api.get<LabValidationReport>('/system/lab-validation');
      setLabValidation(data);
    } finally {
      setLabValidationLoading(false);
    }
  };

  const parseNumberList = (value?: string) => {
    if (!value?.trim()) return undefined;
    return value.split(/[,\s]+/).map(item => Number(item.trim())).filter(value => Number.isInteger(value) && value > 0);
  };

  const parseTextList = (value?: string) => {
    if (!value?.trim()) return undefined;
    return value.split(/[,\s]+/).map(item => item.trim()).filter(Boolean);
  };

  const openLabRunModal = (target?: LabValidationTarget) => {
    labRunForm.resetFields();
    labRunForm.setFieldsValue({
      strict: true,
      check_pxe: true,
      check_bmc: true,
      check_ssh: true,
      limit: target ? 1 : 20,
      server_ids: target ? String(target.server_id) : undefined,
      pxe_macs: target?.primary_mac || undefined,
      ssh_probe_command: undefined,
      pxe_arch: 9
    });
    setLabRunOpen(true);
  };

  const runLabValidation = async () => {
    const values = await labRunForm.validateFields();
    const payload = {
      strict: values.strict,
      check_pxe: values.check_pxe,
      check_bmc: values.check_bmc,
      check_ssh: values.check_ssh,
      limit: values.limit,
      server_ids: parseNumberList(values.server_ids),
      pxe_macs: parseTextList(values.pxe_macs),
      pxe_probe_mac: values.pxe_probe_mac || undefined,
      ssh_probe_command: values.ssh_probe_command || undefined,
      pxe_arch: values.pxe_arch
    };
    setLabValidationRunLoading(true);
    try {
      const { data } = await api.post<LabValidationReport>('/system/lab-validation/run', payload, { headers: { 'X-Confirm-Action': 'system.lab-validation.run' } });
      setLabValidation(data);
      setLabRunOpen(false);
      if (data.status === 'ok') msg.success('真实验收检查通过');
      else if (data.status === 'warning') msg.warning('真实验收检查存在 warning');
      else msg.error('真实验收检查存在 error');
    } finally {
      setLabValidationRunLoading(false);
    }
  };

  const openLabRunDetail = async (run: LabValidationRunSummary) => {
    setLabRunDetail(null);
    setLabRunDetailOpen(true);
    setLabRunDetailLoading(true);
    try {
      const { data } = await api.get<LabValidationRunDetail>(`/system/lab-validation/runs/${run.id}`);
      setLabRunDetail(data);
    } finally {
      setLabRunDetailLoading(false);
    }
  };

  const openLabEvidenceBundle = async (run: LabValidationRunSummary) => {
    setLabEvidenceBundle(null);
    setLabEvidenceBundleOpen(true);
    setLabEvidenceBundleLoading(true);
    try {
      const { data } = await api.get<LabValidationEvidenceBundle>(`/system/lab-validation/runs/${run.id}/evidence-bundle`);
      setLabEvidenceBundle(data);
    } finally {
      setLabEvidenceBundleLoading(false);
    }
  };

  const downloadLabEvidenceBundle = () => {
    if (!labEvidenceBundle) return;
    const blob = new Blob([JSON.stringify(labEvidenceBundle, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `lab-validation-run-${labEvidenceBundle.run.id}.json`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  const openEvidenceModal = (target?: LabValidationTarget) => {
    evidenceForm.resetFields();
    evidenceForm.setFieldsValue({
      kind: target ? 'full' : 'pxe',
      status: 'ok',
      subject: target ? serverLabel(target) : undefined,
      run_id: target?.latest_run_strict ? target.latest_run_id : undefined,
      server_id: target?.server_id,
      boot_event_id: target?.pxe_boot_event_id,
      summary: target ? `${serverLabel(target)} 真实 ${target.bmc_required ? 'PXE/BMC/SSH' : 'PXE/SSH'} 验收证据` : undefined
    });
    setEvidenceOpen(true);
  };

  const checklistBootEvent = (row: LabOperatorChecklistItem) => {
    if (!row.boot_event_id || !labEvidenceBundle) return undefined;
    return labEvidenceBundle.boot_events.find(event => event.id === row.boot_event_id);
  };

  const checklistTarget = (row: LabOperatorChecklistItem) => {
    if (!row.server_id || !labEvidenceBundle) return undefined;
    return labEvidenceBundle.targets.find(target => target.server_id === row.server_id);
  };

  const checklistStepSatisfiedForFull = (serverID: number | undefined, step: string) => {
    if (!serverID || !labEvidenceBundle) return false;
    return labEvidenceBundle.operator_checklist.some(item => item.server_id === serverID && item.step === step && (item.status === 'ok' || (step === 'bmc_identity' && item.status === 'skipped')));
  };

  const canRecordChecklistEvidence = (row: LabOperatorChecklistItem) => {
    if (!labEvidenceBundle || !row.run_id) return false;
    if (row.step === 'pxe_boot_event') return row.status === 'ok' && !!row.boot_event_id && !!checklistBootEvent(row)?.mac;
    if (row.step === 'bmc_identity' || row.step === 'ssh_command') return row.status === 'ok' && !!row.server_id;
    if (row.step === 'full_chain_evidence') {
        return !!row.server_id && !!row.boot_event_id && !row.evidence_id &&
        checklistStepSatisfiedForFull(row.server_id, 'pxe_boot_event') &&
        checklistStepSatisfiedForFull(row.server_id, 'bmc_identity') &&
        checklistStepSatisfiedForFull(row.server_id, 'ssh_command');
    }
    return false;
  };

  const openEvidenceFromChecklist = (row: LabOperatorChecklistItem) => {
    const target = checklistTarget(row);
    const event = checklistBootEvent(row);
    const targetName = target ? serverLabel(target) : row.subject;
    const values: Record<string, unknown> = {
      status: 'ok',
      run_id: row.run_id,
      server_id: row.server_id,
      boot_event_id: row.boot_event_id
    };
    if (row.step === 'pxe_boot_event') {
      values.kind = 'pxe';
      values.subject = event?.mac || row.subject;
      values.summary = `真实 PXE 启动证据 - ${event?.mac || row.subject}`;
      values.details = `验收批次 #${row.run_id} 记录到真实 PXE BootEvent #${row.boot_event_id}${event?.source ? `，来源 ${event.source}` : ''}。`;
    } else if (row.step === 'bmc_identity') {
      values.kind = 'bmc';
      values.subject = targetName;
      values.summary = `物理 Redfish/IPMI 身份证据 - ${targetName}`;
      values.details = `验收批次 #${row.run_id} 包含 server_id ${row.server_id} 的物理 BMC 身份 proof。`;
    } else if (row.step === 'ssh_command') {
      values.kind = 'ssh';
      values.subject = targetName;
      values.summary = `真实 SSH 命令证据 - ${targetName}`;
      values.details = `验收批次 #${row.run_id} 包含 server_id ${row.server_id} 的真实 SSH command/exit_code/stdout proof。`;
    } else {
      values.kind = 'full';
      values.subject = targetName;
      if (target?.bmc_required) {
        values.summary = `全链路 PXE/BMC/SSH 验收证据 - ${targetName}`;
        values.details = `验收批次 #${row.run_id} 已包含 server_id ${row.server_id} 的真实 PXE BootEvent #${row.boot_event_id}、物理 BMC 身份 proof 和真实 SSH 命令 proof。`;
      } else {
        values.summary = `全链路 PXE/SSH 验收证据 - ${targetName}`;
        values.details = `验收批次 #${row.run_id} 已包含 server_id ${row.server_id} 的真实 PXE BootEvent #${row.boot_event_id} 和真实 SSH 命令 proof；该目标未配置 BMC，BMC 按可选能力跳过。`;
      }
    }
    evidenceForm.resetFields();
    evidenceForm.setFieldsValue(values);
    setEvidenceOpen(true);
  };

  const openEvidenceFromCandidate = (candidate: LabEvidenceCandidate) => {
    evidenceForm.resetFields();
    evidenceForm.setFieldsValue({
      kind: candidate.kind,
      status: candidate.status,
      subject: candidate.subject,
      summary: candidate.summary,
      details: candidate.details,
      run_id: candidate.run_id,
      server_id: candidate.server_id,
      boot_event_id: candidate.boot_event_id
    });
    setEvidenceOpen(true);
  };

  const recordLabEvidence = async () => {
    const values = await evidenceForm.validateFields();
    setEvidenceLoading(true);
    try {
      await api.post('/system/lab-validation/evidence', values, { headers: { 'X-Confirm-Action': 'system.lab-validation.evidence' } });
      msg.success('验收证据已记录');
      setEvidenceOpen(false);
      void loadLabValidation();
      if (labEvidenceBundle?.run) void openLabEvidenceBundle(labEvidenceBundle.run);
    } finally {
      setEvidenceLoading(false);
    }
  };

  useEffect(() => { void loadUsers(1, pageSize); void loadTenants(1, tenantPageSize); void loadNetworks(1, networkPageSize); void loadReadiness(); void loadLabValidation(); }, []);

  const createUser = async () => {
    const values = await createForm.validateFields();
    await api.post('/users', values);
    msg.success('用户已创建');
    setCreateOpen(false);
    createForm.resetFields();
    void loadUsers(1, pageSize);
  };

  const apiErrorMessage = (error: unknown, fallback: string) => {
    if (error && typeof error === 'object' && 'response' in error) {
      const response = (error as { response?: { data?: { error?: unknown } } }).response;
      if (typeof response?.data?.error === 'string') return response.data.error;
    }
    return fallback;
  };

  const updateRole = async (user: User, role: string) => {
    try {
      await api.patch(`/users/${user.id}`, { role }, { suppressGlobalError: true });
      msg.success('角色已更新');
    } catch (error) {
      msg.error(apiErrorMessage(error, '角色更新失败'));
    } finally {
      void loadUsers(page, pageSize);
    }
  };

  const openEditUser = (user: User) => {
    setEditingUser(user);
    userForm.setFieldsValue({ name: user.name, role: user.role });
    setUserOpen(true);
  };

  const closeUserModal = () => {
    setUserOpen(false);
    setEditingUser(null);
    userForm.resetFields();
  };

  const saveUser = async () => {
    if (!editingUser) return;
    const values = await userForm.validateFields();
    try {
      await api.patch(`/users/${editingUser.id}`, values, { suppressGlobalError: true });
      msg.success('用户已更新');
      closeUserModal();
      void loadUsers(page, pageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '用户更新失败'));
    }
  };

  const openResetPassword = (user: User) => {
    setActiveUser(user);
    passwordForm.resetFields();
    setPasswordOpen(true);
  };

  const resetPassword = async () => {
    if (!activeUser) return;
    const values = await passwordForm.validateFields();
    await api.post(`/users/${activeUser.id}/reset-password`, values);
    msg.success('密码已重置');
    setPasswordOpen(false);
  };

  const openCreateTenant = () => {
    setEditingTenant(null);
    tenantForm.resetFields();
    tenantForm.setFieldsValue({ status: 'active', quota_text: '{\n  "servers": 10\n}' });
    setTenantOpen(true);
  };

  const openEditTenant = (tenant: Tenant) => {
    setEditingTenant(tenant);
    tenantForm.setFieldsValue({
      tenant_id: tenant.tenant_id,
      name: tenant.name,
      status: tenant.status,
      owner: tenant.owner,
      description: tenant.description,
      quota_text: quotaToText(tenant.quota)
    });
    setTenantOpen(true);
  };

  const closeTenantModal = () => {
    setTenantOpen(false);
    setEditingTenant(null);
    tenantForm.resetFields();
  };

  const saveTenant = async () => {
    const values = await tenantForm.validateFields();
    const payload = { ...values };
    if (typeof payload.quota_text === 'string' && payload.quota_text.trim()) {
      try {
        payload.quota = JSON.parse(payload.quota_text);
      } catch {
        msg.error('配额 JSON 格式无效');
        return;
      }
    }
    delete payload.quota_text;
    if (editingTenant) {
      delete payload.tenant_id;
      await api.patch(`/tenants/${editingTenant.id}`, payload);
      msg.success('租户已更新');
      closeTenantModal();
      void loadTenants(tenantPage, tenantPageSize);
    } else {
      await api.post('/tenants', payload);
      msg.success('租户已创建');
      closeTenantModal();
      void loadTenants(1, tenantPageSize);
    }
  };

  const updateTenantStatus = async (tenant: Tenant, status: string) => {
    await api.patch(`/tenants/${tenant.id}`, { status });
    msg.success('租户状态已更新');
    void loadTenants(tenantPage, tenantPageSize);
  };

  const openCreateNetwork = () => {
    setEditingNetwork(null);
    networkForm.resetFields();
    networkForm.setFieldsValue({ purpose: 'deployment', dhcp_mode: 'proxy', proxy_dhcp: true, status: 'enabled' });
    setNetworkOpen(true);
  };

  const openEditNetwork = (network: NetworkConfig) => {
    setEditingNetwork(network);
    networkForm.setFieldsValue({
      name: network.name,
      purpose: network.purpose,
      cidr: network.cidr,
      gateway: network.gateway,
      dns: network.dns,
      vlan_id: network.vlan_id,
      dhcp_mode: network.dhcp_mode,
      proxy_dhcp: network.proxy_dhcp,
      status: network.status,
      description: network.description
    });
    setNetworkOpen(true);
  };

  const closeNetworkModal = () => {
    setNetworkOpen(false);
    setEditingNetwork(null);
    networkForm.resetFields();
  };

  const saveNetwork = async () => {
    const values = await networkForm.validateFields();
    if (editingNetwork) {
      await api.patch(`/network-configs/${editingNetwork.id}`, values);
      msg.success('网络配置已更新');
      closeNetworkModal();
      void loadNetworks(networkPage, networkPageSize);
    } else {
      await api.post('/network-configs', values);
      msg.success('网络配置已创建');
      closeNetworkModal();
      void loadNetworks(1, networkPageSize);
    }
  };

  const updateNetworkStatus = async (network: NetworkConfig, status: string) => {
    await api.patch(`/network-configs/${network.id}`, { status });
    msg.success('网络状态已更新');
    void loadNetworks(networkPage, networkPageSize);
  };

  const checkNetwork = async (network: NetworkConfig) => {
    setCheckingNetwork(network);
    setNetworkCheck(null);
    setNetworkCheckOpen(true);
    setNetworkCheckLoading(true);
    try {
      const { data } = await api.post<NetworkCheckReport>(`/network-configs/${network.id}/check`, {});
      setNetworkCheck(data);
      if (data.status === 'ok') msg.success('网络检查通过');
      else if (data.status === 'warning') msg.warning('网络检查存在 warning');
      else msg.error('网络检查存在 error');
    } finally {
      setNetworkCheckLoading(false);
    }
  };

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">系统管理</Typography.Title>
    <Tabs items={[{
      key: 'readiness',
      label: <span><SafetyCertificateOutlined />运行自检</span>,
      children: <>
        <div className="toolbar">
          <Space>
            <Tag color={readiness?.status === 'ok' ? 'green' : 'orange'}>总体 {readiness?.status || '-'}</Tag>
            <Button icon={<ReloadOutlined />} loading={readinessLoading} onClick={() => loadReadiness()}>刷新</Button>
          </Space>
        </div>
        <Table<ReadinessCheck> rowKey="name" dataSource={readiness?.checks || []} loading={readinessLoading} pagination={false} columns={[
          { title: '检查项', dataIndex: 'name' },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={labRunColor(value)}>{value}</Tag> },
          { title: '结果', dataIndex: 'message' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>配置问题</Typography.Title>
        <Table<ConfigIssue> rowKey={record => `${record.level}-${record.key}-${record.message}`} dataSource={readiness?.config_issues || []} loading={readinessLoading} pagination={false} locale={{ emptyText: '暂无配置问题' }} columns={[
          { title: '级别', dataIndex: 'level', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
          { title: '配置项', dataIndex: 'key' },
          { title: '说明', dataIndex: 'message' }
        ]} />
      </>
    }, {
      key: 'lab-validation',
      label: <span><ExperimentOutlined />真实验收</span>,
      children: <>
        <div className="toolbar">
          <Space>
            <Tag color={readinessColor(labValidation?.status || 'warning')}>总体 {labValidation?.status || '-'}</Tag>
            <Button icon={<ReloadOutlined />} loading={labValidationLoading} onClick={() => loadLabValidation()}>刷新</Button>
            <Button type="primary" icon={<PlayCircleOutlined />} loading={labValidationRunLoading} onClick={() => openLabRunModal()}>执行检查</Button>
            <Button icon={<FileDoneOutlined />} onClick={() => openEvidenceModal()}>记录证据</Button>
          </Space>
        </div>
        <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} style={{ marginBottom: 16 }}>
          <Descriptions.Item label="环境">{labValidation?.environment.app_env || '-'}</Descriptions.Item>
          <Descriptions.Item label="生成时间">{labValidation?.generated_at || '-'}</Descriptions.Item>
          <Descriptions.Item label="BMC 适配器">{labValidation?.environment.bmc_adapter || '-'}</Descriptions.Item>
          <Descriptions.Item label="启动 URL">{labValidation?.environment.boot_base_url || '-'}</Descriptions.Item>
          <Descriptions.Item label="采集模式">{labValidation?.environment.collector_mode || '-'}</Descriptions.Item>
          <Descriptions.Item label="SSH 运维模式">{labValidation?.environment.ssh_operations_mode || '-'}</Descriptions.Item>
        </Descriptions>
        <Table<LabValidationCheck> rowKey="name" dataSource={labValidation?.checks || []} loading={labValidationLoading || labValidationRunLoading} pagination={false} columns={[
          { title: '检查项', dataIndex: 'name' },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
          { title: '结果', dataIndex: 'message' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>PXE/DHCP/TFTP</Typography.Title>
        <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} style={{ marginBottom: 12 }}>
          <Descriptions.Item label="服务">{labValidation?.pxe.enabled ? <Tag color="green">enabled</Tag> : <Tag>disabled</Tag>}</Descriptions.Item>
          <Descriptions.Item label="模式">{labValidation?.pxe.mode || '-'}</Descriptions.Item>
          <Descriptions.Item label="网卡/VLAN">{labValidation?.pxe.bind_interface || '-'}</Descriptions.Item>
          <Descriptions.Item label="DHCP 监听">{labValidation?.pxe.dhcp_listen_addr || '-'}</Descriptions.Item>
          <Descriptions.Item label="DHCP Server IP">{labValidation?.pxe.dhcp_server_ip || '-'}</Descriptions.Item>
          <Descriptions.Item label="TFTP 监听">{labValidation?.pxe.tftp_listen_addr || '-'}</Descriptions.Item>
          <Descriptions.Item label="TFTP 根目录">{labValidation?.pxe.tftp_root || '-'}</Descriptions.Item>
          <Descriptions.Item label="部署网段">{labValidation?.pxe.deployment_networks ?? '-'}</Descriptions.Item>
          <Descriptions.Item label="启动事件">{labValidation?.pxe.boot_events ?? '-'}</Descriptions.Item>
          <Descriptions.Item label="启动文件">{`${labValidation?.pxe.bootfile_uefi || '-'} / ${labValidation?.pxe.bootfile_bios || '-'}`}</Descriptions.Item>
        </Descriptions>
        <Table<LabBootEvent> rowKey="id" dataSource={labValidation?.pxe.recent_boot_events || []} loading={labValidationLoading} pagination={false} size="small" locale={{ emptyText: '暂无启动事件' }} columns={[
          { title: 'MAC', dataIndex: 'mac' },
          { title: '固件', dataIndex: 'firmware' },
          { title: '架构', dataIndex: 'architecture' },
          { title: '链路', dataIndex: 'source', render: value => <Tag color={value === 'api_event' ? 'orange' : 'blue'}>{value || 'unknown'}</Tag> },
          { title: '远端', dataIndex: 'remote_addr' },
          { title: '资产', dataIndex: 'server_id', render: value => value || '-' },
          { title: '时间', dataIndex: 'created_at' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>BMC</Typography.Title>
        <Descriptions bordered size="small" column={{ xs: 1, md: 4 }} style={{ marginBottom: 12 }}>
          <Descriptions.Item label="适配器">{labValidation?.bmc.adapter || '-'}</Descriptions.Item>
          <Descriptions.Item label="总数">{labValidation?.bmc.total ?? '-'}</Descriptions.Item>
          <Descriptions.Item label="ok">{labValidation?.bmc.ok ?? '-'}</Descriptions.Item>
          <Descriptions.Item label="error">{labValidation?.bmc.error ?? '-'}</Descriptions.Item>
        </Descriptions>
        <Table<LabBMCRef> rowKey="server_id" dataSource={labValidation?.bmc.recent_endpoints || []} loading={labValidationLoading || labValidationRunLoading} pagination={false} size="small" locale={{ emptyText: '暂无 BMC 端点' }} columns={[
          { title: '资产', render: (_, row) => serverLabel(row) },
          { title: '类型', dataIndex: 'type' },
          { title: '端点', dataIndex: 'endpoint' },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value === 'ok' ? 'ok' : value === 'error' ? 'error' : 'warning')}>{value || 'unknown'}</Tag> },
          { title: '电源', dataIndex: 'power_state' },
          { title: '最近检查', dataIndex: 'last_checked_at', render: value => value || '-' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>SSH</Typography.Title>
        <Descriptions bordered size="small" column={{ xs: 1, md: 4 }} style={{ marginBottom: 12 }}>
          <Descriptions.Item label="采集">{labValidation?.ssh.collector_mode || '-'}</Descriptions.Item>
          <Descriptions.Item label="运维">{labValidation?.ssh.operations_mode || '-'}</Descriptions.Item>
          <Descriptions.Item label="总数">{labValidation?.ssh.total ?? '-'}</Descriptions.Item>
          <Descriptions.Item label="ok">{labValidation?.ssh.ok ?? '-'}</Descriptions.Item>
        </Descriptions>
        <Table<LabSSHRef> rowKey="server_id" dataSource={labValidation?.ssh.recent_ssh_accesses || []} loading={labValidationLoading || labValidationRunLoading} pagination={false} size="small" locale={{ emptyText: '暂无 SSH 配置' }} columns={[
          { title: '资产', render: (_, row) => serverLabel(row) },
          { title: '主机', render: (_, row) => `${row.host}:${row.port || 22}` },
          { title: '用户', dataIndex: 'username' },
          { title: '认证', dataIndex: 'auth_type' },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value === 'ok' ? 'ok' : value === 'error' ? 'error' : 'warning')}>{value || 'unknown'}</Tag> },
          { title: '最近检查', dataIndex: 'last_checked_at', render: value => value || '-' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>目标矩阵</Typography.Title>
        <Table<LabValidationTarget> rowKey="server_id" dataSource={labValidation?.targets || []} loading={labValidationLoading || labValidationRunLoading} pagination={false} size="small" locale={{ emptyText: '暂无物理验收目标' }} columns={[
          { title: '资产', render: (_, row) => serverLabel(row) },
          { title: 'MAC', dataIndex: 'primary_mac', render: value => value || '-' },
          { title: 'PXE', dataIndex: 'pxe_status', render: (_, row) => <Space><Tag color={labTargetStatusColor(row.pxe_status)}>{row.pxe_status}</Tag>{row.pxe_boot_event_id ? <Typography.Text type="secondary">#{row.pxe_boot_event_id}</Typography.Text> : null}</Space> },
          { title: 'BMC', render: (_, row) => bmcTargetCell(row) },
          { title: 'SSH', dataIndex: 'ssh_status', render: value => <Tag color={labTargetStatusColor(value)}>{value}</Tag> },
          { title: '证据', dataIndex: 'evidence_status', render: (_, row) => <Space><Tag color={labTargetStatusColor(row.evidence_status)}>{row.evidence_status}</Tag>{row.evidence_id ? <Typography.Text type="secondary">#{row.evidence_id}</Typography.Text> : null}</Space> },
          { title: '最近批次', render: (_, row) => row.latest_run_id ? <Space><Typography.Text>#{row.latest_run_id}</Typography.Text>{row.latest_run_strict ? <Tag color="red">strict</Tag> : null}{row.latest_run_status ? <Tag color={labRunColor(row.latest_run_status)}>{row.latest_run_status}</Tag> : null}{row.latest_run_kind ? <Typography.Text type="secondary">{row.latest_run_kind}</Typography.Text> : null}{row.latest_run_result_status ? <Tag color={runResultColor(row.latest_run_result_status)}>{row.latest_run_result_status}</Tag> : null}</Space> : '-' },
          { title: '全链路', dataIndex: 'full_chain_ready', render: value => value ? <Tag color="green">ready</Tag> : <Tag color="orange">pending</Tag> },
          { title: '缺口', dataIndex: 'blocking_reasons', render: value => value?.length ? value.join('；') : '-' },
          { title: '操作', render: (_, row) => <Space><Button size="small" icon={<PlayCircleOutlined />} loading={labValidationRunLoading} onClick={() => openLabRunModal(row)}>严格检查</Button><Button size="small" icon={<FileDoneOutlined />} onClick={() => openEvidenceModal(row)}>补证据</Button></Space> }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>物理证据</Typography.Title>
        <Table<LabValidationEvidence> rowKey="id" dataSource={labValidation?.recent_evidence || []} loading={labValidationLoading} pagination={false} size="small" locale={{ emptyText: '暂无物理证据' }} columns={[
          { title: '类型', dataIndex: 'kind' },
          { title: '对象', dataIndex: 'subject', render: value => value || '-' },
          { title: '资产', dataIndex: 'server_id', render: value => value || '-' },
          { title: 'BootEvent', dataIndex: 'boot_event_id', render: value => value || '-' },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
          { title: '摘要', dataIndex: 'summary' },
          { title: '批次', dataIndex: 'run_id', render: value => value ? `#${value}` : '-' },
          { title: '记录人', dataIndex: 'created_by' },
          { title: '时间', dataIndex: 'created_at' }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>执行批次</Typography.Title>
        <Table<LabValidationRunSummary> rowKey="id" dataSource={labValidation?.recent_runs || []} loading={labValidationLoading || labValidationRunLoading} pagination={false} size="small" locale={{ emptyText: '暂无执行批次' }} columns={[
          { title: '批次', dataIndex: 'id', render: value => `#${value}` },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={labRunColor(value)}>{value}</Tag> },
          { title: '严格', dataIndex: 'strict', render: value => value ? <Tag color="red">strict</Tag> : <Tag>off</Tag> },
          { title: '范围', render: (_, row) => labRunTargets(row) },
          { title: '结果', render: (_, row) => `${row.results} 项 / ${row.failures} 失败 / ${row.skipped} 跳过` },
          { title: '请求人', dataIndex: 'requested_by', render: value => value || '-' },
          { title: 'Request ID', dataIndex: 'request_id', render: value => value || '-' },
          { title: '开始时间', dataIndex: 'started_at' },
          { title: '完成时间', dataIndex: 'finished_at', render: value => value || '-' },
          { title: '操作', render: (_, row) => <Space><Button size="small" icon={<SearchOutlined />} onClick={() => openLabRunDetail(row)}>查看</Button><Button size="small" icon={<DownloadOutlined />} onClick={() => openLabEvidenceBundle(row)}>验收包</Button></Space> }
        ]} />
        <Typography.Title level={5} style={{ marginTop: 18 }}>执行结果</Typography.Title>
        <Table<LabValidationRunResult> rowKey={record => record.id || `${record.run_id || labValidation?.run_id || 'current'}-${record.kind}-${record.server_id}-${record.message}`} dataSource={labValidation?.run_results || []} loading={labValidationRunLoading} pagination={false} size="small" locale={{ emptyText: '暂无执行结果' }} columns={[
          { title: '批次', dataIndex: 'run_id', render: value => value || labValidation?.run_id || '-' },
          { title: '类型', dataIndex: 'kind' },
          { title: '资产', render: (_, row) => serverLabel(row) },
          { title: '状态', dataIndex: 'status', render: value => <Tag color={runResultColor(value)}>{value}</Tag> },
          { title: '结果', dataIndex: 'message' },
          { title: '详情', dataIndex: 'details', render: value => <Typography.Text code>{runResultDetails(value)}</Typography.Text> },
          { title: '时间', dataIndex: 'checked_at', render: value => value || '-' }
        ]} />
      </>
    }, {
      key: 'users',
      label: '用户与角色',
      forceRender: true,
      children: <>
        <div className="toolbar">
          <Space><Button type="primary" icon={<PlusOutlined />} onClick={() => { createForm.setFieldsValue({ role: 'operator' }); setCreateOpen(true); }}>新建用户</Button><Button icon={<ReloadOutlined />} onClick={() => loadUsers(page, pageSize)}>刷新</Button></Space>
          <Form form={filterForm} layout="inline" onFinish={() => loadUsers(1, pageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="邮箱/姓名" allowClear /></Form.Item>
            <Form.Item name="role"><Select placeholder="角色" allowClear style={{ width: 140 }} options={['admin', 'operator', 'viewer'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { filterForm.resetFields(); void loadUsers(1, pageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={users} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => loadUsers(p, ps) }} columns={[
          { title: '邮箱', dataIndex: 'email' },
          { title: '姓名', dataIndex: 'name' },
          { title: '角色', dataIndex: 'role', render: v => <Tag color={roleColor(v)}>{v}</Tag> },
          { title: '创建时间', dataIndex: 'created_at' },
          { title: '角色调整', render: (_, r) => <Select size="small" value={r.role} style={{ width: 130 }} onChange={(value) => updateRole(r, value)} options={['admin', 'operator', 'viewer'].map(v => ({ value: v, label: v }))} /> },
          { title: '操作', render: (_, r) => <Space><Button size="small" icon={<EditOutlined />} onClick={() => openEditUser(r)}>编辑</Button><Button size="small" icon={<LockOutlined />} onClick={() => openResetPassword(r)}>重置密码</Button></Space> }
        ]} />
      </>
    }, {
      key: 'tenants',
      label: '租户管理',
      forceRender: true,
      children: <>
        <div className="toolbar">
          <Space><Button type="primary" icon={<PlusOutlined />} onClick={openCreateTenant}>新建租户</Button><Button icon={<ReloadOutlined />} onClick={() => loadTenants(tenantPage, tenantPageSize)}>刷新</Button></Space>
          <Form form={tenantFilterForm} layout="inline" onFinish={() => loadTenants(1, tenantPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="租户/名称/负责人" allowClear /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 140 }} options={['active', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { tenantFilterForm.resetFields(); void loadTenants(1, tenantPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={tenants} pagination={{ current: tenantPage, pageSize: tenantPageSize, total: tenantTotal, showSizeChanger: true, onChange: (p, ps) => loadTenants(p, ps) }} columns={[
          { title: '租户 ID', dataIndex: 'tenant_id' },
          { title: '名称', dataIndex: 'name' },
          { title: '状态', dataIndex: 'status', render: v => <Tag color={v === 'active' ? 'green' : 'default'}>{v}</Tag> },
          { title: '负责人', dataIndex: 'owner' },
          { title: '描述', dataIndex: 'description' },
          { title: '配额', dataIndex: 'quota', render: v => v ? JSON.stringify(v) : '-' },
          { title: '状态调整', render: (_, r) => <Select size="small" value={r.status} style={{ width: 120 }} onChange={(value) => updateTenantStatus(r, value)} options={['active', 'disabled'].map(v => ({ value: v, label: v }))} /> },
          { title: '操作', render: (_, r) => <Button size="small" icon={<EditOutlined />} onClick={() => openEditTenant(r)}>编辑</Button> }
        ]} />
      </>
    }, {
      key: 'networks',
      label: '网络配置',
      forceRender: true,
      children: <>
        <div className="toolbar">
          <Space><Button type="primary" icon={<PlusOutlined />} onClick={openCreateNetwork}>新建网络</Button><Button icon={<ReloadOutlined />} onClick={() => loadNetworks(networkPage, networkPageSize)}>刷新</Button></Space>
          <Form form={networkFilterForm} layout="inline" onFinish={() => loadNetworks(1, networkPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="名称/CIDR/描述" allowClear /></Form.Item>
            <Form.Item name="purpose"><Select placeholder="用途" allowClear style={{ width: 140 }} options={['management', 'deployment', 'business'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 140 }} options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { networkFilterForm.resetFields(); void loadNetworks(1, networkPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={networks} pagination={{ current: networkPage, pageSize: networkPageSize, total: networkTotal, showSizeChanger: true, onChange: (p, ps) => loadNetworks(p, ps) }} columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '用途', dataIndex: 'purpose', render: v => <Tag color={v === 'deployment' ? 'blue' : v === 'management' ? 'purple' : 'green'}>{v}</Tag> },
          { title: 'CIDR', dataIndex: 'cidr' },
          { title: '网关', dataIndex: 'gateway' },
          { title: 'DNS', dataIndex: 'dns' },
          { title: 'VLAN', dataIndex: 'vlan_id', render: v => v || '-' },
          { title: 'DHCP', dataIndex: 'dhcp_mode', render: (_, r) => `${r.dhcp_mode}${r.proxy_dhcp ? ' / Proxy' : ''}` },
          { title: '状态', dataIndex: 'status', render: v => <Tag color={v === 'enabled' ? 'green' : 'default'}>{v}</Tag> },
          { title: '状态调整', render: (_, r) => <Select size="small" value={r.status} style={{ width: 120 }} onChange={(value) => updateNetworkStatus(r, value)} options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /> },
          { title: '操作', render: (_, r) => <Space><Button size="small" icon={<SafetyCertificateOutlined />} onClick={() => checkNetwork(r)}>检查</Button><Button size="small" icon={<EditOutlined />} onClick={() => openEditNetwork(r)}>编辑</Button></Space> }
        ]} />
      </>
    }]} />

    <Modal title="新建用户" open={createOpen} onOk={createUser} onCancel={() => setCreateOpen(false)} width={620} forceRender>
      <Form form={createForm} layout="vertical">
        <Form.Item name="email" label="邮箱" rules={[{ required: true }, { type: 'email' }]}><Input /></Form.Item>
        <Form.Item name="name" label="姓名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="role" label="角色" rules={[{ required: true }]}><Select options={['admin', 'operator', 'viewer'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="password" label="初始密码" rules={[{ required: true }, { min: 8 }]}><Input.Password /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`编辑用户 - ${editingUser?.email || ''}`} open={userOpen} onOk={saveUser} onCancel={closeUserModal} width={560} forceRender>
      <Form form={userForm} layout="vertical">
        <Form.Item name="name" label="姓名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="role" label="角色" rules={[{ required: true }]}><Select options={['admin', 'operator', 'viewer'].map(v => ({ value: v, label: v }))} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`重置密码 - ${activeUser?.email || ''}`} open={passwordOpen} onOk={resetPassword} onCancel={() => setPasswordOpen(false)} forceRender>
      <Form form={passwordForm} layout="vertical">
        <Form.Item name="password" label="新密码" rules={[{ required: true }, { min: 8 }]}><Input.Password /></Form.Item>
      </Form>
    </Modal>

    <Modal title={editingTenant ? `编辑租户 - ${editingTenant.tenant_id}` : '新建租户'} open={tenantOpen} onOk={saveTenant} onCancel={closeTenantModal} width={680} forceRender>
      <Form form={tenantForm} layout="vertical">
        <Form.Item name="tenant_id" label="租户 ID" rules={[{ required: true }]}><Input placeholder="tenant-a" disabled={!!editingTenant} /></Form.Item>
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="status" label="状态"><Select options={['active', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="owner" label="负责人"><Input /></Form.Item>
        <Form.Item name="description" label="描述"><Input.TextArea rows={3} /></Form.Item>
        <Form.Item name="quota_text" label="配额 JSON"><Input.TextArea rows={4} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`网络检查 - ${checkingNetwork?.name || ''}`} open={networkCheckOpen} onCancel={() => setNetworkCheckOpen(false)} footer={<Button type="primary" onClick={() => setNetworkCheckOpen(false)}>关闭</Button>} width={720}>
      <Space style={{ marginBottom: 12 }}>
        <Tag color={readinessColor(networkCheck?.status || 'warning')}>总体 {networkCheck?.status || '-'}</Tag>
        <Typography.Text>{checkingNetwork?.cidr || ''}</Typography.Text>
      </Space>
      <Table<NetworkCheck> rowKey="name" loading={networkCheckLoading} dataSource={networkCheck?.checks || []} pagination={false} columns={[
        { title: '检查项', dataIndex: 'name' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '结果', dataIndex: 'message' }
      ]} />
    </Modal>

    <Modal title="记录物理验收证据" open={evidenceOpen} onOk={recordLabEvidence} confirmLoading={evidenceLoading} onCancel={() => setEvidenceOpen(false)} width={680} forceRender>
      <Form form={evidenceForm} layout="vertical">
        <Form.Item name="kind" label="类型" rules={[{ required: true }]}><Select options={[
          { value: 'pxe', label: 'PXE/DHCP/TFTP' },
          { value: 'bmc', label: 'Redfish/IPMI' },
          { value: 'ssh', label: 'SSH 主机' },
          { value: 'full', label: '全链路' }
        ]} /></Form.Item>
        <Form.Item name="status" label="状态" rules={[{ required: true }]}><Select options={[
          { value: 'ok', label: 'ok' },
          { value: 'warning', label: 'warning' },
          { value: 'error', label: 'error' }
        ]} /></Form.Item>
        <Form.Item name="subject" label="对象" rules={[{ required: true, message: '请输入证据对象' }, { max: 160 }]}><Input placeholder="MAC、BMC 地址、SSH 主机或批次编号" /></Form.Item>
        <Space wrap>
          <Form.Item name="run_id" label="验收批次 ID" rules={[({ getFieldValue }) => ({
            validator(_, value) {
              const kind = getFieldValue('kind');
              if (getFieldValue('status') === 'ok' && ['bmc', 'ssh', 'full'].includes(kind) && !value) {
                return Promise.reject(new Error('ok BMC、SSH 或全链路证据必须关联严格验收批次 ID'));
              }
              return Promise.resolve();
            }
          })]}><InputNumber min={1} /></Form.Item>
          <Form.Item name="server_id" label="资产 ID" rules={[({ getFieldValue }) => ({
            validator(_, value) {
              const kind = getFieldValue('kind');
              if (getFieldValue('status') === 'ok' && ['bmc', 'ssh', 'full'].includes(kind) && !value) {
                return Promise.reject(new Error('ok BMC、SSH 或全链路证据必须关联资产 ID'));
              }
              return Promise.resolve();
            }
          })]}><InputNumber min={1} /></Form.Item>
          <Form.Item name="boot_event_id" label="BootEvent ID" rules={[({ getFieldValue }) => ({
            validator(_, value) {
              const kind = getFieldValue('kind');
              if (getFieldValue('status') === 'ok' && ['pxe', 'full'].includes(kind) && !value) {
                return Promise.reject(new Error('ok PXE 或全链路证据必须关联 BootEvent ID'));
              }
              return Promise.resolve();
            }
          })]}><InputNumber min={1} /></Form.Item>
        </Space>
        <Form.Item name="summary" label="摘要" rules={[{ required: true }, { max: 500 }]}><Input /></Form.Item>
        <Form.Item name="details" label="详情" rules={[{ max: 4000 }]}><Input.TextArea rows={5} /></Form.Item>
        <Form.Item name="artifact_url" label="证据链接" rules={[{ type: 'url' }, { max: 500 }]}><Input placeholder="https://..." /></Form.Item>
      </Form>
    </Modal>

    <Modal title="执行真实验收检查" open={labRunOpen} onOk={runLabValidation} confirmLoading={labValidationRunLoading} onCancel={() => setLabRunOpen(false)} width={680} forceRender>
      <Form form={labRunForm} layout="vertical">
        <Space wrap>
          <Form.Item name="strict" label="严格验收" style={{ minWidth: 130 }}><Select options={[{ value: true, label: '开启' }, { value: false, label: '关闭' }]} /></Form.Item>
          <Form.Item name="check_pxe" label="PXE" style={{ minWidth: 130 }}><Select options={[{ value: true, label: '检查' }, { value: false, label: '跳过' }]} /></Form.Item>
          <Form.Item name="check_bmc" label="BMC" style={{ minWidth: 130 }}><Select options={[{ value: true, label: '检查' }, { value: false, label: '跳过' }]} /></Form.Item>
          <Form.Item name="check_ssh" label="SSH" style={{ minWidth: 130 }}><Select options={[{ value: true, label: '检查' }, { value: false, label: '跳过' }]} /></Form.Item>
          <Form.Item name="limit" label="数量上限"><InputNumber min={1} max={50} /></Form.Item>
        </Space>
        <Form.Item name="server_ids" label="资产 ID"><Input placeholder="1,2,3" /></Form.Item>
        <Form.Item name="pxe_macs" label="PXE MAC"><Input placeholder="52:54:00:aa:bb:cc, 52:54:00:dd:ee:ff" /></Form.Item>
        <Form.Item name="ssh_probe_command" label="SSH 探针命令" rules={[{ max: 255 }]}><Input placeholder="默认执行安全只读探针命令" /></Form.Item>
        <Space wrap>
          <Form.Item name="pxe_probe_mac" label="探针 MAC"><Input placeholder="52:54:00:00:00:fe" style={{ width: 240 }} /></Form.Item>
          <Form.Item name="pxe_arch" label="PXE 架构" style={{ minWidth: 180 }}><Select options={[
            { value: 9, label: 'UEFI x86_64 (9)' },
            { value: 7, label: 'UEFI x86_64 (7)' },
            { value: 11, label: 'UEFI x86_64 HTTP (11)' },
            { value: 0, label: 'Legacy BIOS (0)' }
          ]} /></Form.Item>
        </Space>
      </Form>
    </Modal>

    <Modal title={`验收批次 #${labRunDetail?.run.id || ''}`} open={labRunDetailOpen} onCancel={() => setLabRunDetailOpen(false)} footer={<Space><Button icon={<DownloadOutlined />} disabled={!labRunDetail} onClick={() => labRunDetail && openLabEvidenceBundle(labRunDetail.run)}>验收包</Button><Button type="primary" onClick={() => setLabRunDetailOpen(false)}>关闭</Button></Space>} width={880}>
      <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} style={{ marginBottom: 12 }}>
        <Descriptions.Item label="状态">{labRunDetail?.run.status ? <Tag color={labRunColor(labRunDetail.run.status)}>{labRunDetail.run.status}</Tag> : '-'}</Descriptions.Item>
        <Descriptions.Item label="严格验收">{labRunDetail?.run.strict ? '开启' : '关闭'}</Descriptions.Item>
        <Descriptions.Item label="请求人">{labRunDetail?.run.requested_by || '-'}</Descriptions.Item>
        <Descriptions.Item label="Request ID">{labRunDetail?.run.request_id || '-'}</Descriptions.Item>
        <Descriptions.Item label="目标">{labRunDetail ? labRunTargets(labRunDetail.run) : '-'}</Descriptions.Item>
        <Descriptions.Item label="SSH 探针命令">{labRunDetail?.run.ssh_probe_command || '-'}</Descriptions.Item>
        <Descriptions.Item label="结果">{labRunDetail ? `${labRunDetail.run.results} 项 / ${labRunDetail.run.failures} 失败 / ${labRunDetail.run.skipped} 跳过` : '-'}</Descriptions.Item>
      </Descriptions>
      <Table<LabValidationRunResult> rowKey={record => record.id || `${record.kind}-${record.server_id}-${record.message}`} dataSource={labRunDetail?.results || []} loading={labRunDetailLoading} pagination={false} size="small" locale={{ emptyText: '暂无执行结果' }} columns={[
        { title: '类型', dataIndex: 'kind' },
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={runResultColor(value)}>{value}</Tag> },
        { title: '结果', dataIndex: 'message' },
        { title: '详情', dataIndex: 'details', render: value => <Typography.Text code>{runResultDetails(value)}</Typography.Text> },
        { title: '时间', dataIndex: 'checked_at', render: value => value || '-' }
      ]} />
    </Modal>

    <Modal title={`验收包 #${labEvidenceBundle?.run.id || ''}`} open={labEvidenceBundleOpen} onCancel={() => setLabEvidenceBundleOpen(false)} footer={<Space><Button icon={<DownloadOutlined />} disabled={!labEvidenceBundle} onClick={downloadLabEvidenceBundle}>下载 JSON</Button><Button type="primary" onClick={() => setLabEvidenceBundleOpen(false)}>关闭</Button></Space>} width={980}>
      <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} style={{ marginBottom: 12 }}>
        <Descriptions.Item label="生成时间">{labEvidenceBundle?.generated_at || '-'}</Descriptions.Item>
        <Descriptions.Item label="批次状态">{labEvidenceBundle?.run.status ? <Tag color={labRunColor(labEvidenceBundle.run.status)}>{labEvidenceBundle.run.status}</Tag> : '-'}</Descriptions.Item>
        <Descriptions.Item label="BMC 适配器">{labEvidenceBundle?.environment.bmc_adapter || '-'}</Descriptions.Item>
        <Descriptions.Item label="启动 URL">{labEvidenceBundle?.environment.boot_base_url || '-'}</Descriptions.Item>
        <Descriptions.Item label="目标">{labEvidenceBundle ? labRunTargets(labEvidenceBundle.run) : '-'}</Descriptions.Item>
        <Descriptions.Item label="备注">{labEvidenceBundle?.notes?.join('；') || '-'}</Descriptions.Item>
      </Descriptions>
      <Typography.Title level={5}>检查项</Typography.Title>
      <Table<LabValidationCheck> rowKey="name" dataSource={labEvidenceBundle?.checks || []} loading={labEvidenceBundleLoading} pagination={false} size="small" columns={[
        { title: '检查项', dataIndex: 'name' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '结果', dataIndex: 'message' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>现场检查清单</Typography.Title>
      <Table<LabOperatorChecklistItem> rowKey={record => `${record.subject}-${record.step}-${record.server_id || 0}-${record.boot_event_id || 0}`} dataSource={labEvidenceBundle?.operator_checklist || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无现场检查清单' }} columns={[
        { title: '对象', dataIndex: 'subject' },
        { title: '步骤', dataIndex: 'step' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={value === 'pending' ? 'orange' : value === 'skipped' ? 'default' : readinessColor(value)}>{value}</Tag> },
        { title: '引用', render: (_, row) => <Space wrap>{row.run_id ? <Typography.Text>run #{row.run_id}</Typography.Text> : null}{row.server_id ? <Typography.Text>server #{row.server_id}</Typography.Text> : null}{row.boot_event_id ? <Typography.Text>BootEvent #{row.boot_event_id}</Typography.Text> : null}{row.evidence_id ? <Typography.Text>evidence #{row.evidence_id}</Typography.Text> : null}</Space> },
        { title: '结果', dataIndex: 'message' },
        { title: '下一步', dataIndex: 'next_action', render: value => value || '-' },
        { title: '阻塞原因', dataIndex: 'blocking_reasons', render: value => Array.isArray(value) && value.length ? value.join('；') : '-' },
        { title: '操作', render: (_, row) => canRecordChecklistEvidence(row) ? <Button size="small" icon={<FileDoneOutlined />} onClick={() => openEvidenceFromChecklist(row)}>记录证据</Button> : '-' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>可记录证据候选</Typography.Title>
      <Table<LabEvidenceCandidate> rowKey={record => `${record.kind}-${record.server_id || 0}-${record.boot_event_id || 0}-${record.source_step}`} dataSource={labEvidenceBundle?.evidence_candidates || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无可记录证据候选' }} columns={[
        { title: '类型', dataIndex: 'kind' },
        { title: '对象', dataIndex: 'subject' },
        { title: '来源', dataIndex: 'source_step' },
        { title: '引用', render: (_, row) => <Space wrap><Typography.Text>run #{row.run_id}</Typography.Text>{row.server_id ? <Typography.Text>server #{row.server_id}</Typography.Text> : null}{row.boot_event_id ? <Typography.Text>BootEvent #{row.boot_event_id}</Typography.Text> : null}</Space> },
        { title: '摘要', dataIndex: 'summary' },
        { title: '操作', render: (_, row) => <Button size="small" icon={<FileDoneOutlined />} onClick={() => openEvidenceFromCandidate(row)}>记录证据</Button> }
      ]} />
      <Typography.Title level={5}>执行结果</Typography.Title>
      <Table<LabValidationRunResult> rowKey={record => record.id || `${record.kind}-${record.server_id}-${record.message}`} dataSource={labEvidenceBundle?.results || []} loading={labEvidenceBundleLoading} pagination={false} size="small" columns={[
        { title: '类型', dataIndex: 'kind' },
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={runResultColor(value)}>{value}</Tag> },
        { title: '结果', dataIndex: 'message' },
        { title: '详情', dataIndex: 'details', render: value => <Typography.Text code>{runResultDetails(value)}</Typography.Text> },
        { title: '时间', dataIndex: 'checked_at', render: value => value || '-' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>目标矩阵</Typography.Title>
      <Table<LabValidationTarget> rowKey="server_id" dataSource={labEvidenceBundle?.targets || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无目标矩阵' }} columns={[
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: 'MAC', dataIndex: 'primary_mac', render: value => value || '-' },
        { title: 'PXE', dataIndex: 'pxe_status', render: value => <Tag color={labTargetStatusColor(value)}>{value}</Tag> },
        { title: 'BMC', render: (_, row) => bmcTargetCell(row) },
        { title: 'SSH', dataIndex: 'ssh_status', render: value => <Tag color={labTargetStatusColor(value)}>{value}</Tag> },
        { title: '证据', dataIndex: 'evidence_status', render: value => <Tag color={labTargetStatusColor(value)}>{value}</Tag> },
        { title: '最近批次', render: (_, row) => row.latest_run_id ? <Space><Typography.Text>#{row.latest_run_id}</Typography.Text>{row.latest_run_strict ? <Tag color="red">strict</Tag> : null}{row.latest_run_status ? <Tag color={labRunColor(row.latest_run_status)}>{row.latest_run_status}</Tag> : null}{row.latest_run_result_status ? <Tag color={runResultColor(row.latest_run_result_status)}>{row.latest_run_result_status}</Tag> : null}</Space> : '-' },
        { title: '全链路', dataIndex: 'full_chain_ready', render: value => value ? <Tag color="green">ready</Tag> : <Tag color="orange">pending</Tag> }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联 BootEvent</Typography.Title>
      <Table<LabBootEvent> rowKey="id" dataSource={labEvidenceBundle?.boot_events || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联 BootEvent' }} columns={[
        { title: 'ID', dataIndex: 'id' },
        { title: 'MAC', dataIndex: 'mac' },
        { title: '固件', dataIndex: 'firmware' },
        { title: '链路', dataIndex: 'source', render: value => <Tag color={value === 'api_event' ? 'orange' : 'blue'}>{value || 'unknown'}</Tag> },
        { title: '远端', dataIndex: 'remote_addr' },
        { title: '资产', dataIndex: 'server_id', render: value => value || '-' },
        { title: '时间', dataIndex: 'created_at' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联 BMC / SSH</Typography.Title>
      <Table<LabBMCRef> rowKey="server_id" dataSource={labEvidenceBundle?.bmc_endpoints || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联 BMC' }} columns={[
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '类型', dataIndex: 'type' },
        { title: '端点', dataIndex: 'endpoint' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '检查时间', dataIndex: 'last_checked_at', render: value => value || '-' }
      ]} />
      <Table<LabSSHRef> rowKey="server_id" dataSource={labEvidenceBundle?.ssh_accesses || []} loading={labEvidenceBundleLoading} pagination={false} size="small" style={{ marginTop: 8 }} locale={{ emptyText: '暂无关联 SSH' }} columns={[
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '主机', render: (_, row) => `${row.host}:${row.port}` },
        { title: '用户', dataIndex: 'username' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '检查时间', dataIndex: 'last_checked_at', render: value => value || '-' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联终端会话</Typography.Title>
      <Table<LabValidationTerminalSession> rowKey="id" dataSource={labEvidenceBundle?.terminal_sessions || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联终端会话' }} columns={[
        { title: 'ID', dataIndex: 'id', render: value => `#${value}` },
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '模式', dataIndex: 'mode' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '操作者', dataIndex: 'requested_by' },
        { title: '打开时间', dataIndex: 'opened_at' },
        { title: 'Transcript', dataIndex: 'transcript', render: value => <Typography.Text code>{value ? String(value).slice(0, 120) : '-'}</Typography.Text> }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联脚本执行</Typography.Title>
      <Table<LabValidationScriptExecution> rowKey="id" dataSource={labEvidenceBundle?.script_executions || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联脚本执行' }} columns={[
        { title: 'ID', dataIndex: 'id', render: value => `#${value}` },
        { title: '任务', render: (_, row) => row.job_name ? `${row.job_name} (#${row.script_job_id})` : `#${row.script_job_id}` },
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={runResultColor(value)}>{value}</Tag> },
        { title: '退出码', dataIndex: 'exit_code' },
        { title: '完成时间', dataIndex: 'finished_at', render: value => value || '-' },
        { title: '输出', render: (_, row) => <Typography.Text code>{(row.stdout || row.stderr) ? `${row.stdout || ''}${row.stderr ? ` ${row.stderr}` : ''}`.slice(0, 120) : '-'}</Typography.Text> }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联日志事件</Typography.Title>
      <Table<LabValidationLogEvent> rowKey="id" dataSource={labEvidenceBundle?.log_events || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联日志事件' }} columns={[
        { title: 'ID', dataIndex: 'id', render: value => `#${value}` },
        { title: '资产', render: (_, row) => serverLabel(row) },
        { title: '来源', dataIndex: 'source' },
        { title: '级别', dataIndex: 'level', render: value => <Tag color={readinessColor(value === 'info' ? 'ok' : 'warning')}>{value}</Tag> },
        { title: '时间', dataIndex: 'occurred_at' },
        { title: '消息', dataIndex: 'message', render: value => <Typography.Text code>{value ? String(value).slice(0, 140) : '-'}</Typography.Text> }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>关联物理证据</Typography.Title>
      <Table<LabValidationEvidence> rowKey="id" dataSource={labEvidenceBundle?.recent_evidence || []} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无关联物理证据' }} columns={[
        { title: '类型', dataIndex: 'kind' },
        { title: '对象', dataIndex: 'subject' },
        { title: '资产', dataIndex: 'server_id', render: value => value || '-' },
        { title: 'BootEvent', dataIndex: 'boot_event_id', render: value => value || '-' },
        { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '摘要', dataIndex: 'summary' },
        { title: '批次', dataIndex: 'run_id', render: value => value ? `#${value}` : '-' },
        { title: '时间', dataIndex: 'created_at' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 18 }}>配置与运行问题</Typography.Title>
      <Table<ConfigIssue> rowKey={record => `${record.level}-${record.key}-${record.message}`} dataSource={[...(labEvidenceBundle?.config_issues || []), ...(labEvidenceBundle?.pxe_runtime_issues || [])]} loading={labEvidenceBundleLoading} pagination={false} size="small" locale={{ emptyText: '暂无配置或运行问题' }} columns={[
        { title: '级别', dataIndex: 'level', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
        { title: '配置项', dataIndex: 'key' },
        { title: '说明', dataIndex: 'message' }
      ]} />
    </Modal>

    <Modal title={editingNetwork ? `编辑网络配置 - ${editingNetwork.name}` : '新建网络配置'} open={networkOpen} onOk={saveNetwork} onCancel={closeNetworkModal} width={720} forceRender>
      <Form form={networkForm} layout="vertical">
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input placeholder="Deployment VLAN 100" /></Form.Item>
        <Form.Item name="purpose" label="用途" rules={[{ required: true }]}><Select options={['management', 'deployment', 'business'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="cidr" label="CIDR" rules={[{ required: true }]}><Input placeholder="192.168.100.0/24" /></Form.Item>
        <Form.Item name="gateway" label="网关"><Input placeholder="192.168.100.1" /></Form.Item>
        <Form.Item name="dns" label="DNS"><Input placeholder="192.168.100.1,8.8.8.8" /></Form.Item>
        <Form.Item name="vlan_id" label="VLAN ID"><InputNumber min={0} max={4094} style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="dhcp_mode" label="DHCP 模式"><Select options={['proxy', 'builtin', 'external'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="proxy_dhcp" label="ProxyDHCP"><Select options={[{ value: true, label: '启用' }, { value: false, label: '关闭' }]} /></Form.Item>
        <Form.Item name="status" label="状态"><Select options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="description" label="描述"><Input.TextArea rows={3} /></Form.Item>
      </Form>
    </Modal>
  </>;
}
