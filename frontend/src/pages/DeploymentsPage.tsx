import { ReloadOutlined, FileTextOutlined, StopOutlined, RedoOutlined } from '@ant-design/icons';
import { Button, Checkbox, Descriptions, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, Deployment, Image, InstallTemplate, NetworkConfig, PageResult, Server, WorkflowTemplate } from '../api/client';
import { canManage } from '../authz';

type DeploymentLog = {
  summary?: { deployment_id: number; status: string; latest_run_id?: number; total_runs: number; started_at?: string; finished_at?: string; duration_ms?: number; task_total: number; task_success: number; task_failed: number; task_cancelled: number; task_running: number; task_pending: number };
  workflow?: WorkflowRunLog | null;
  runs?: WorkflowRunLog[];
  tasks: Array<{ id: number; workflow_run_id?: number; step_name: string; action: string; status: string; stdout: string; stderr: string; error_message?: string; started_at?: string; finished_at?: string; duration_ms?: number }>;
};

type WorkflowRunLog = { id: number; attempt: number; name: string; status: string; started_at?: string; finished_at?: string; duration_ms?: number; task_total: number; task_success: number; task_failed: number; task_cancelled: number; task_running: number; task_pending: number };

const statusColor = (status: string) => {
  if (status === 'success') return 'green';
  if (status === 'running') return 'blue';
  if (status === 'cancelled') return 'orange';
  if (status === 'failed') return 'red';
  return 'default';
};

const deployableServerStatuses = ['ready', 'running', 'maintenance'];
const erasePolicyLabels: Record<string, string> = { none: '不执行擦除', quick: '快速擦除', full: '全盘擦除', external_verified: '外部已验证' };
const erasePolicyColor = (policy?: string) => ({ none: 'default', quick: 'blue', full: 'red', external_verified: 'green' }[policy || ''] || 'default');
const formatDuration = (startedAt?: string, finishedAt?: string) => {
  if (!startedAt) return '-';
  const started = Date.parse(startedAt);
  const finished = finishedAt ? Date.parse(finishedAt) : Date.now();
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) return '-';
  const seconds = Math.round((finished - started) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes < 60) return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const minuteRest = minutes % 60;
  return minuteRest ? `${hours}h ${minuteRest}m` : `${hours}h`;
};
const formatDurationMs = (durationMs?: number) => {
  if (typeof durationMs !== 'number' || !Number.isFinite(durationMs)) return '-';
  if (durationMs > 0 && durationMs < 1000) return `${Math.round(durationMs)}ms`;
  const seconds = Math.max(0, Math.round(durationMs / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes < 60) return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const minuteRest = minutes % 60;
  return minuteRest ? `${hours}h ${minuteRest}m` : `${hours}h`;
};

const deploymentFailure = (deployment: Deployment | null, logData: DeploymentLog) => {
  if (deployment?.error_message) return deployment.error_message;
  const failedTask = logData.tasks.find(task => task.status === 'failed' && (task.error_message || task.stderr));
  return failedTask?.error_message || failedTask?.stderr || '-';
};

export function DeploymentsPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const [rows, setRows] = useState<Deployment[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [servers, setServers] = useState<Server[]>([]);
  const [images, setImages] = useState<Image[]>([]);
  const [installTemplates, setInstallTemplates] = useState<InstallTemplate[]>([]);
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([]);
  const [networks, setNetworks] = useState<NetworkConfig[]>([]);
  const [open, setOpen] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const [active, setActive] = useState<Deployment | null>(null);
  const [logData, setLogData] = useState<DeploymentLog>({ tasks: [] });
  const [form] = Form.useForm();
  const [filterForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const load = async (nextPage = page, nextPageSize = pageSize) => {
    const values = filterForm.getFieldsValue();
    const { data } = await api.get<PageResult<Deployment>>('/deployments', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRows(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };
  useEffect(() => { void load(1, pageSize); }, []);

  const openCreate = async () => {
    const [serverRes, imageRes, installRes, workflowRes, networkRes] = await Promise.all([
      api.get('/servers'),
      api.get('/images'),
      api.get('/install-templates'),
      api.get('/workflow-templates'),
      api.get('/network-configs', { params: { purpose: 'deployment', status: 'enabled' } })
    ]);
    const deployableServers = serverRes.data.filter((s: Server) => deployableServerStatuses.includes(s.status));
    const deployableImages = imageRes.data.filter((i: Image) => i.status === 'enabled' && i.test_status === 'tested_passed');
    const deploymentNetworks = networkRes.data.filter((n: NetworkConfig) => n.purpose === 'deployment' && n.status === 'enabled');
    setServers(deployableServers);
    setImages(deployableImages);
    setInstallTemplates(installRes.data.filter((t: InstallTemplate) => t.status === 'enabled'));
    setWorkflowTemplates(workflowRes.data.filter((t: WorkflowTemplate) => t.status === 'enabled'));
    setNetworks(deploymentNetworks);
    if (deployableServers.length === 0) msg.warning('没有可部署状态的服务器资产');
    if (deployableImages.length === 0) msg.warning('没有已启用且校验通过的镜像');
    if (deploymentNetworks.length === 0) msg.warning('没有启用的部署网络');
    form.resetFields();
    form.setFieldsValue({ variables: '{"hostname":"node-01"}', network_id: deploymentNetworks[0]?.id, erase_policy: 'quick', erase_confirmed: false });
    setOpen(true);
  };

  const create = async () => {
    const values = await form.validateFields().catch(() => null);
    if (!values) return;
    let variables = undefined;
    try { variables = values.variables ? JSON.parse(values.variables) : undefined; } catch { msg.error('部署变量 JSON 无效'); return; }
    const serverIDs = values.server_ids as number[];
    const { server_ids: _serverIDs, ...rest } = values;
    if (serverIDs.length === 1) {
      await api.post('/deployments', { ...rest, server_id: serverIDs[0], variables }, { headers: { 'X-Confirm-Action': 'deployment.create' } });
      msg.success('部署任务已创建');
    } else {
      await api.post('/deployments/batch', { ...rest, server_ids: serverIDs, variables }, { headers: { 'X-Confirm-Action': 'deployment.batch-create' } });
      msg.success(`已创建 ${serverIDs.length} 个部署任务`);
    }
    setOpen(false);
    form.resetFields();
    setTimeout(() => load(1, pageSize), 500);
  };

  const selectServers = (serverIDs: number[]) => {
    if (serverIDs.length !== 1) return;
    const server = servers.find(s => s.id === serverIDs[0]);
    if (!server) return;
    let variables: Record<string, unknown> = {};
    const current = form.getFieldValue('variables');
    try { variables = current ? JSON.parse(current) : {}; } catch { variables = {}; }
    variables.hostname = server.hostname || server.asset_no || `server-${server.id}`;
    if (server.primary_ip) variables.primary_ip = server.primary_ip;
    if (server.primary_mac) variables.primary_mac = server.primary_mac;
    form.setFieldValue('variables', JSON.stringify(variables, null, 2));
  };

  const cancel = async (row: Deployment) => {
    await api.post(`/deployments/${row.id}/cancel`, {}, { headers: { 'X-Confirm-Action': 'deployment.cancel' } });
    msg.success('部署任务已取消');
    await load(page, pageSize);
    if (active?.id === row.id) await showDetail(row.id);
  };

  const retry = async (row: Deployment) => {
    await api.post(`/deployments/${row.id}/retry`, {}, { headers: { 'X-Confirm-Action': 'deployment.retry' } });
    msg.success('部署任务已重新入队');
    await load(page, pageSize);
    if (active?.id === row.id) await showDetail(row.id);
  };

  const showDetail = async (id: number) => {
    const [detailRes, logRes] = await Promise.all([api.get(`/deployments/${id}`), api.get(`/deployments/${id}/logs`, { suppressGlobalError: true }).catch(() => ({ data: { tasks: [] } }))]);
    setActive(detailRes.data);
    setLogData({ summary: logRes.data.summary, workflow: logRes.data.workflow, runs: logRes.data.runs || [], tasks: logRes.data.tasks || [] });
    setDetailOpen(true);
  };

  const canCancel = (status: string) => status === 'pending' || status === 'running';
  const canRetry = (status: string) => status === 'failed' || status === 'cancelled';

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">部署任务</Typography.Title>
    <div className="toolbar">
      <Space>{canWrite && <Button type="primary" onClick={openCreate}>创建部署</Button>}<Button icon={<ReloadOutlined />} onClick={() => load(page, pageSize)}>刷新</Button></Space>
      <Form form={filterForm} layout="inline" onFinish={() => load(1, pageSize)} style={{ marginTop: 12 }}>
        <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 140 }} options={['pending', 'running', 'success', 'failed', 'cancelled'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="server_id"><Input placeholder="服务器 ID" allowClear /></Form.Item>
        <Form.Item name="image_id"><Input placeholder="镜像 ID" allowClear /></Form.Item>
        <Form.Item name="network_id"><Input placeholder="网络 ID" allowClear /></Form.Item>
        <Form.Item name="requested_by"><Input placeholder="发起人" allowClear /></Form.Item>
        <Space>
          <Button type="primary" htmlType="submit">查询</Button>
          <Button onClick={() => { filterForm.resetFields(); void load(1, pageSize); }}>重置</Button>
        </Space>
      </Form>
    </div>
    <Table rowKey="id" dataSource={rows} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => load(p, ps) }} columns={[
      { title: 'ID', dataIndex: 'id' },
      { title: '服务器', dataIndex: 'server_id' },
      { title: '镜像', dataIndex: 'image_id' },
      { title: '网络', dataIndex: 'network_id', render: v => v || '-' },
      { title: '擦除策略', dataIndex: 'erase_policy', render: v => <Tag color={erasePolicyColor(v)}>{erasePolicyLabels[v] || v || '-'}</Tag> },
      { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
      { title: '发起人', dataIndex: 'requested_by' },
      { title: '耗时', render: (_, r) => formatDuration(r.started_at, r.finished_at) },
      { title: '创建时间', dataIndex: 'created_at' },
      { title: '操作', render: (_, r) => <Space>
        <Button size="small" icon={<FileTextOutlined />} onClick={() => showDetail(r.id)}>详情</Button>
        {canWrite && canCancel(r.status) && <Popconfirm title="确认取消该部署任务？" onConfirm={() => cancel(r)}><Button size="small" icon={<StopOutlined />}>取消</Button></Popconfirm>}
        {canWrite && canRetry(r.status) && <Popconfirm title="确认重试该部署任务？会按原擦除策略重新进入安装流程。" onConfirm={() => retry(r)}><Button size="small" icon={<RedoOutlined />}>重试</Button></Popconfirm>}
      </Space> }
    ]} />

    <Modal title="创建部署任务" open={open} onOk={create} onCancel={() => setOpen(false)} width={680} forceRender>
      <Form form={form} layout="vertical">
        <Form.Item name="server_ids" label="服务器" rules={[{ required: true, message: '请选择服务器' }, { type: 'array', max: 20, message: '单批最多 20 台服务器' }]}>
          <Select mode="multiple" onChange={selectServers} options={servers.map(s => ({ value: s.id, label: `${s.hostname || s.asset_no} (${s.status})` }))} />
        </Form.Item>
        <Form.Item name="image_id" label="镜像" rules={[{ required: true }]}><Select options={images.map(i => ({ value: i.id, label: `${i.name} ${i.os_version} (${i.test_status})` }))} /></Form.Item>
        <Form.Item name="network_id" label="部署网络"><Select allowClear options={networks.map(n => ({ value: n.id, label: `${n.name} ${n.cidr}${n.vlan_id ? ` VLAN ${n.vlan_id}` : ''}` }))} /></Form.Item>
        <Form.Item name="template_id" label="安装模板"><Select allowClear options={installTemplates.map(t => ({ value: t.id, label: `${t.name} (${t.template_type})` }))} /></Form.Item>
        <Form.Item name="workflow_id" label="工作流模板"><Select allowClear options={workflowTemplates.map(t => ({ value: t.id, label: `${t.name} ${t.version}` }))} /></Form.Item>
        <Form.Item name="erase_policy" label="擦除策略" rules={[{ required: true }]}>
          <Select options={[
            { value: 'quick', label: '快速擦除' },
            { value: 'full', label: '全盘擦除' },
            { value: 'external_verified', label: '外部已验证' },
            { value: 'none', label: '不执行擦除' }
          ]} />
        </Form.Item>
        <Form.Item name="erase_confirmed" valuePropName="checked" rules={[{ validator: (_, checked) => checked ? Promise.resolve() : Promise.reject(new Error('请确认磁盘擦除/重装风险')) }]}>
          <Checkbox>确认本次部署会重装系统并可能覆盖目标磁盘数据</Checkbox>
        </Form.Item>
        <Form.Item name="variables" label="部署变量 JSON"><Input.TextArea rows={5} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`部署详情 #${active?.id || ''}`} open={detailOpen} footer={<Space><Button icon={<ReloadOutlined />} onClick={() => active && showDetail(active.id)}>刷新</Button><Button type="primary" onClick={() => setDetailOpen(false)}>关闭</Button></Space>} onCancel={() => setDetailOpen(false)} width={900}>
      <Descriptions column={2} size="small" bordered>
        <Descriptions.Item label="状态"><Tag color={statusColor(active?.status || '')}>{active?.status || '-'}</Tag></Descriptions.Item>
        <Descriptions.Item label="发起人">{active?.requested_by || '-'}</Descriptions.Item>
        <Descriptions.Item label="服务器">{active?.server_id || '-'}</Descriptions.Item>
        <Descriptions.Item label="镜像">{active?.image_id || '-'}</Descriptions.Item>
        <Descriptions.Item label="部署网络">{active?.network_id || '-'}</Descriptions.Item>
        <Descriptions.Item label="擦除策略"><Tag color={erasePolicyColor(active?.erase_policy)}>{active?.erase_policy ? erasePolicyLabels[active.erase_policy] || active.erase_policy : '-'}</Tag></Descriptions.Item>
        <Descriptions.Item label="擦除确认">{active?.erase_confirmed ? active.erase_confirmed_at || '已确认' : '未确认'}</Descriptions.Item>
        <Descriptions.Item label="工作流">{logData.workflow?.name || '-'}</Descriptions.Item>
        <Descriptions.Item label="工作流状态"><Tag color={statusColor(logData.workflow?.status || '')}>{logData.workflow?.status || '-'}</Tag></Descriptions.Item>
        <Descriptions.Item label="开始时间">{active?.started_at || '-'}</Descriptions.Item>
        <Descriptions.Item label="结束时间">{active?.finished_at || '-'}</Descriptions.Item>
        <Descriptions.Item label="运行次数">{logData.summary?.total_runs ?? '-'}</Descriptions.Item>
        <Descriptions.Item label="任务统计">{logData.summary ? `${logData.summary.task_success}/${logData.summary.task_total} success` : '-'}</Descriptions.Item>
        <Descriptions.Item label="任务耗时">{formatDurationMs(logData.summary?.duration_ms) !== '-' ? formatDurationMs(logData.summary?.duration_ms) : formatDuration(active?.started_at, active?.finished_at)}</Descriptions.Item>
        <Descriptions.Item label="失败原因">{deploymentFailure(active, logData)}</Descriptions.Item>
      </Descriptions>
      <Typography.Title level={5} style={{ marginTop: 16 }}>运行记录</Typography.Title>
      <Table size="small" pagination={false} rowKey="id" dataSource={logData.runs || []} columns={[
        { title: '尝试', dataIndex: 'attempt' },
        { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
        { title: '步骤', render: (_, r) => `${r.task_success}/${r.task_total}` },
        { title: '耗时', render: (_, r) => formatDurationMs(r.duration_ms) },
        { title: '开始时间', dataIndex: 'started_at' },
        { title: '结束时间', dataIndex: 'finished_at' }
      ]} />
      <Typography.Title level={5} style={{ marginTop: 16 }}>步骤日志</Typography.Title>
      <Table pagination={false} rowKey="id" dataSource={logData.tasks} columns={[
        { title: '步骤', dataIndex: 'step_name' },
        { title: '动作', dataIndex: 'action' },
        { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
        { title: '耗时', render: (_, r) => formatDurationMs(r.duration_ms) !== '-' ? formatDurationMs(r.duration_ms) : formatDuration(r.started_at, r.finished_at) },
        { title: '输出/错误', render: (_, r) => <Typography.Text type={r.status === 'failed' ? 'danger' : undefined}>{r.error_message || r.stderr || r.stdout || '-'}</Typography.Text> }
      ]} />
    </Modal>
  </>;
}
