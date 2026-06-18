import { DeleteOutlined, DownloadOutlined, EditOutlined, HddOutlined, ImportOutlined, LineChartOutlined, ReloadOutlined, SafetyCertificateOutlined, StopOutlined, ThunderboltOutlined, ToolOutlined } from '@ant-design/icons';
import { Button, Descriptions, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, BMCFirmwareInfo, CollectionJob, MetricSample, PageResult, RetirementRecord, Server, SSHAccess, Tenant } from '../api/client';
import { canManage, isAdmin } from '../authz';

type BOMRow = {
  asset_no: string;
  hostname: string;
  cpu_summary: string;
  memory_summary: string;
  disk_summary: string;
  network_summary: string;
  gpu_summary: string;
  raid_summary: string;
  collected_by: string;
  collected_at?: string;
};

type StatusHistoryRow = {
  id: number;
  from_status: string;
  to_status: string;
  reason: string;
  actor_email: string;
  created_at: string;
};

type BatchPowerResponse = {
  requested: number;
  succeeded: number;
  failed: number;
  results: Array<{ server_id: number; status: string; power_state?: string; error?: string }>;
};

type TerminalLifecycleAction = 'retire' | 'scrap';

const serverStatuses = ['discovered', 'ready', 'deploying', 'running', 'maintenance', 'retired', 'scrapped'];
const terminalStatuses = ['retired', 'scrapped'];
const eraseStatusOptions = [
  { value: 'not_required', label: 'not_required' },
  { value: 'pending', label: 'pending' },
  { value: 'verified', label: 'verified' },
  { value: 'failed', label: 'failed' }
];
const isTerminalStatus = (status?: string) => Boolean(status && terminalStatuses.includes(status));
const isTerminalServer = (server?: Server | null) => isTerminalStatus(server?.status);
const canScrapServer = (server?: Server | null) => server?.status !== 'scrapped';
const batchConfirmHeader = (action: string) => action === 'reboot' ? 'bmc.batch-reboot' : `bmc.batch-${action}`;
const batchPowerLabel = (action: string) => ({ 'power-on': '开机', 'power-off': '关机', reboot: '重启' }[action] || action);
const eraseStatusColor = (status: string) => ({ verified: 'green', pending: 'orange', failed: 'red', not_required: 'default' }[status] || 'default');
const lifecycleActionLabel = (action: TerminalLifecycleAction) => action === 'scrap' ? '报废' : '退役';
const tagsToText = (tags: unknown) => {
  if (tags === undefined || tags === null || tags === '') return '';
  try {
    return JSON.stringify(tags, null, 2);
  } catch {
    return String(tags);
  }
};
const tagsToInline = (tags: unknown) => {
  if (tags === undefined || tags === null || tags === '') return '';
  try {
    return JSON.stringify(tags);
  } catch {
    return String(tags);
  }
};

export function ServersPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const admin = isAdmin(role);
  const [rows, setRows] = useState<Server[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Server | null>(null);
  const [bmcOpen, setBmcOpen] = useState(false);
  const [inventoryOpen, setInventoryOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [bomOpen, setBomOpen] = useState(false);
  const [firmwareOpen, setFirmwareOpen] = useState(false);
  const [firmwareLoading, setFirmwareLoading] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [retireOpen, setRetireOpen] = useState(false);
  const [monitorOpen, setMonitorOpen] = useState(false);
  const [sshOpen, setSshOpen] = useState(false);
  const [activeServer, setActiveServer] = useState<Server | null>(null);
  const [terminalAction, setTerminalAction] = useState<TerminalLifecycleAction>('retire');
  const [bom, setBom] = useState<BOMRow | null>(null);
  const [firmware, setFirmware] = useState<BMCFirmwareInfo | null>(null);
  const [history, setHistory] = useState<StatusHistoryRow[]>([]);
  const [retirementRecords, setRetirementRecords] = useState<RetirementRecord[]>([]);
  const [metrics, setMetrics] = useState<MetricSample[]>([]);
  const [collections, setCollections] = useState<CollectionJob[]>([]);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [selectedServerIDs, setSelectedServerIDs] = useState<number[]>([]);
  const [form] = Form.useForm();
  const [filterForm] = Form.useForm();
  const [bmcForm] = Form.useForm();
  const [sshForm] = Form.useForm();
  const [inventoryForm] = Form.useForm();
  const [importForm] = Form.useForm();
  const [retirementForm] = Form.useForm();
  const retireEraseStatus = Form.useWatch('erase_status', retirementForm);
  const [msg, holder] = message.useMessage();

  const load = async (nextPage = page, nextPageSize = pageSize) => {
    const values = filterForm.getFieldsValue();
    const { data } = await api.get<PageResult<Server>>('/servers', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRows(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };

  const loadTenantOptions = async () => {
    const { data } = await api.get<PageResult<Tenant>>('/tenants', { params: { page: 1, page_size: 100 } });
    setTenants(data.items);
  };

  useEffect(() => { void load(1, pageSize); void loadTenantOptions(); }, []);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ status: 'discovered', architecture: 'x86_64', tenant_id: tenants.find(tenant => tenant.status === 'active')?.tenant_id, tags_text: '[]' });
    setOpen(true);
  };

  const openEdit = (server: Server) => {
    setEditing(server);
    form.setFieldsValue({ ...server, tags_text: tagsToText(server.tags) });
    setOpen(true);
  };

  const persistServer = async (values: Record<string, unknown>, terminalConfirmed = false) => {
    const payload = { ...values };
    if ('tags_text' in payload) {
      const rawTags = String(payload.tags_text || '').trim();
      if (rawTags) {
        try {
          payload.tags = JSON.parse(rawTags);
        } catch {
          msg.error('标签 JSON 格式无效');
          return;
        }
      } else {
        payload.tags = null;
      }
      delete payload.tags_text;
    }
    if (editing) {
      await api.patch(`/servers/${editing.id}`, payload, terminalConfirmed ? { headers: { 'X-Confirm-Action': 'server.status-terminal' } } : undefined);
      msg.success('资产已更新');
    } else {
      await api.post('/servers', payload);
      msg.success('资产已创建');
    }
    setOpen(false);
    setEditing(null);
    form.resetFields();
    void load(editing ? page : 1, pageSize);
  };

  const save = async () => {
    const values = form.getFieldsValue();
    if (editing && values.status !== editing.status && isTerminalStatus(String(values.status || ''))) {
      Modal.confirm({
        title: '确认将资产设为终态？',
        okText: '确认',
        cancelText: '取消',
        okButtonProps: { danger: true },
        onOk: () => persistServer(values, true)
      });
      return;
    }
    await persistServer(values);
  };

  const openTerminalLifecycle = (server: Server, action: TerminalLifecycleAction) => {
    setActiveServer(server);
    setTerminalAction(action);
    retirementForm.resetFields();
    retirementForm.setFieldsValue({ reason: '', erase_status: 'pending', erase_method: '', evidence: '' });
    setRetireOpen(true);
  };

  const submitTerminalLifecycle = async () => {
    if (!activeServer) return;
    const values = await retirementForm.validateFields();
    await api.post(`/servers/${activeServer.id}/${terminalAction}`, values, { headers: { 'X-Confirm-Action': `server.${terminalAction}` } });
    msg.success(`资产已${lifecycleActionLabel(terminalAction)}`);
    setRetireOpen(false);
    retirementForm.resetFields();
    setSelectedServerIDs(ids => ids.filter(id => id !== activeServer.id));
    void load(page, pageSize);
  };

  const deleteServer = async (server: Server) => {
    await api.delete(`/servers/${server.id}`, { headers: { 'X-Confirm-Action': 'server.delete' } });
    msg.success('资产已删除');
    setSelectedServerIDs(ids => ids.filter(id => id !== server.id));
    await load(page, pageSize);
  };

  const power = async (id: number, action: string) => {
    try {
      await api.post(`/servers/${id}/bmc/${action}`, {}, { headers: { 'X-Confirm-Action': `bmc.${action}` }, suppressGlobalError: true });
      msg.success('BMC 操作已执行');
    } catch {
      msg.error('请先配置该资产的 BMC 端点');
    }
  };

  const batchPower = async (action: string) => {
    if (selectedServerIDs.length === 0) {
      msg.warning('请先选择资产');
      return;
    }
    const { data } = await api.post<BatchPowerResponse>('/servers/bmc/batch-power', { action, server_ids: selectedServerIDs }, { headers: { 'X-Confirm-Action': batchConfirmHeader(action) } });
    if (data.failed > 0) {
      msg.warning(`批量${batchPowerLabel(action)}完成：成功 ${data.succeeded}，失败 ${data.failed}`);
    } else {
      msg.success(`批量${batchPowerLabel(action)}已完成：成功 ${data.succeeded}`);
    }
    setSelectedServerIDs([]);
    await load(page, pageSize);
  };

  const getPower = async (server: Server) => {
    try {
      const res = await api.get<{ power_state: string; adapter: string }>(`/servers/${server.id}/bmc/power`, { suppressGlobalError: true });
      msg.info(`${server.hostname} 当前电源状态: ${res.data.power_state || 'unknown'}`);
    } catch {
      msg.error('请先配置该资产的 BMC 端点');
    }
  };

  const checkBmc = async (server: Server) => {
    try {
      const res = await api.post<{ status: string; checked_at?: string }>(`/servers/${server.id}/bmc/check`, {}, { suppressGlobalError: true });
      const checkedAt = res.data.checked_at ? `，检查时间: ${res.data.checked_at}` : '';
      msg.success(`${server.hostname} BMC 连通性正常，状态: ${res.data.status || 'ok'}${checkedAt}`);
    } catch {
      msg.error('请先配置该资产的 BMC 端点');
    }
  };

  const configureBmc = (server: Server) => {
    setActiveServer(server);
    bmcForm.setFieldsValue({ type: 'redfish', protocol: 'https', endpoint: 'https://192.168.100.201', username: 'admin', password: '' });
    setBmcOpen(true);
  };

  const saveBmc = async () => {
    if (!activeServer) return;
    await api.post(`/servers/${activeServer.id}/bmc`, bmcForm.getFieldsValue(), { headers: { 'X-Confirm-Action': 'bmc.upsert' } });
    msg.success('BMC 端点已保存');
    setBmcOpen(false);
  };

  const openInventory = (server: Server) => {
    setActiveServer(server);
    inventoryForm.setFieldsValue({ collected_by: 'manual', cpu_summary: '', memory_summary: '', disk_summary: '', network_summary: '', gpu_summary: '', raid_summary: '' });
    setInventoryOpen(true);
  };

  const saveInventory = async () => {
    if (!activeServer) return;
    await api.post(`/servers/${activeServer.id}/inventory`, inventoryForm.getFieldsValue());
    msg.success('硬件信息已记录');
    setInventoryOpen(false);
  };

  const importServers = async () => {
    const raw = importForm.getFieldValue('payload');
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      msg.error('JSON 格式无效');
      return;
    }
    const servers = Array.isArray(parsed) ? parsed : (parsed as { servers?: unknown }).servers;
    if (!Array.isArray(servers)) {
      msg.error('导入内容必须是数组');
      return;
    }
    const res = await api.post('/servers/import', { servers });
    msg.success(`已导入 ${res.data.created} 台资产`);
    setImportOpen(false);
    importForm.resetFields();
    void load(1, pageSize);
  };

  const showBOM = async (server: Server) => {
    setActiveServer(server);
    const res = await api.get(`/servers/${server.id}/bom`);
    setBom(res.data);
    setBomOpen(true);
  };

  const showFirmware = async (server: Server) => {
    setActiveServer(server);
    setFirmware(null);
    setFirmwareOpen(true);
    setFirmwareLoading(true);
    try {
      const { data } = await api.get<BMCFirmwareInfo>(`/servers/${server.id}/bmc/firmware`, { suppressGlobalError: true });
      setFirmware(data);
    } catch {
      msg.error('BMC 固件信息查询失败，请检查端点或凭据');
      setFirmwareOpen(false);
    } finally {
      setFirmwareLoading(false);
    }
  };

  const showHistory = async (server: Server) => {
    setActiveServer(server);
    const [historyRes, retirementRes] = await Promise.all([
      api.get<StatusHistoryRow[]>(`/servers/${server.id}/status-history`),
      api.get<RetirementRecord[]>(`/servers/${server.id}/retirement-records`)
    ]);
    setHistory(historyRes.data);
    setRetirementRecords(retirementRes.data);
    setHistoryOpen(true);
  };

  const loadServerMonitoring = async (server: Server) => {
    const [metricRes, collectionRes] = await Promise.all([
      api.get<MetricSample[]>(`/servers/${server.id}/metrics`),
      api.get<CollectionJob[]>(`/servers/${server.id}/collections`)
    ]);
    setMetrics(metricRes.data);
    setCollections(collectionRes.data);
  };

  const showMonitor = async (server: Server) => {
    setActiveServer(server);
    await loadServerMonitoring(server);
    setMonitorOpen(true);
  };

  const refreshMonitor = async () => {
    if (!activeServer) return;
    await loadServerMonitoring(activeServer);
    msg.success('监控数据已刷新');
  };

  const startCollection = async () => {
    if (!activeServer) return;
    await api.post(`/servers/${activeServer.id}/collections`);
    msg.success('采集任务已启动');
    await loadServerMonitoring(activeServer);
  };

  const openSSH = async (server: Server) => {
    setActiveServer(server);
    sshForm.resetFields();
    try {
      const { data } = await api.get<SSHAccess>(`/servers/${server.id}/ssh`, { suppressGlobalError: true });
      sshForm.setFieldsValue({ host: data.host, port: data.port || 22, username: data.username, auth_type: data.auth_type || 'password', secret: '' });
    } catch {
      sshForm.setFieldsValue({ host: server.primary_ip || '', port: 22, username: 'root', auth_type: 'password', secret: '' });
    }
    setSshOpen(true);
  };

  const saveSSH = async () => {
    if (!activeServer) return;
    const values = await sshForm.validateFields();
    await api.post(`/servers/${activeServer.id}/ssh`, values, { headers: { 'X-Confirm-Action': 'ssh.upsert' } });
    msg.success('SSH 采集配置已保存');
    setSshOpen(false);
  };

  const downloadBlob = (data: BlobPart, filename: string, type = 'text/csv;charset=utf-8') => {
    const url = URL.createObjectURL(new Blob([data], { type }));
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  };

  const downloadBOM = async (server?: Server) => {
    const path = server ? `/servers/${server.id}/bom.csv` : '/bom.csv';
    const res = await api.get(path, { responseType: 'blob' });
    downloadBlob(res.data, server ? `server-bom-${server.id}.csv` : 'baremetal-bom.csv');
  };

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">资产管理</Typography.Title>
    <div className="toolbar">
      <Space wrap>
        {canWrite && <Button type="primary" onClick={openCreate}>新建资产</Button>}
        {canWrite && <Button icon={<ImportOutlined />} onClick={() => setImportOpen(true)}>批量导入</Button>}
        {canWrite && <Popconfirm title={`确认批量打开 ${selectedServerIDs.length} 台资产电源？`} onConfirm={() => batchPower('power-on')}><Button disabled={selectedServerIDs.length === 0}>批量开机</Button></Popconfirm>}
        {canWrite && <Popconfirm title={`确认批量关闭 ${selectedServerIDs.length} 台资产电源？`} onConfirm={() => batchPower('power-off')}><Button danger disabled={selectedServerIDs.length === 0}>批量关机</Button></Popconfirm>}
        {canWrite && <Popconfirm title={`确认批量重启 ${selectedServerIDs.length} 台资产？`} onConfirm={() => batchPower('reboot')}><Button disabled={selectedServerIDs.length === 0}>批量重启</Button></Popconfirm>}
        <Button icon={<DownloadOutlined />} onClick={() => downloadBOM()}>导出全量 BOM</Button>
      </Space>
      <Form form={filterForm} layout="inline" onFinish={() => load(1, pageSize)} style={{ marginTop: 12 }}>
        <Form.Item name="keyword"><Input placeholder="资产/主机/IP/MAC" allowClear /></Form.Item>
        <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 140 }} options={serverStatuses.map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="owner"><Input placeholder="负责人" allowClear /></Form.Item>
        <Form.Item name="tenant_id"><Select placeholder="租户" allowClear showSearch style={{ width: 160 }} optionFilterProp="label" options={tenants.map(tenant => ({ value: tenant.tenant_id, label: `${tenant.tenant_id} · ${tenant.name}` }))} /></Form.Item>
        <Space>
          <Button type="primary" htmlType="submit">查询</Button>
          <Button onClick={() => { filterForm.resetFields(); void load(1, pageSize); }}>重置</Button>
        </Space>
      </Form>
    </div>
    <Table rowKey="id" dataSource={rows} rowSelection={canWrite ? { selectedRowKeys: selectedServerIDs, onChange: keys => setSelectedServerIDs(keys.map(Number)), getCheckboxProps: record => ({ disabled: isTerminalServer(record) }) } : undefined} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => load(p, ps) }} columns={[
      { title: '资产编号', dataIndex: 'asset_no' },
      { title: '主机名', dataIndex: 'hostname' },
      { title: '状态', dataIndex: 'status', render: (v) => <Tag>{v}</Tag> },
      { title: '架构', dataIndex: 'architecture' },
      { title: 'IP', dataIndex: 'primary_ip' },
      { title: 'MAC', dataIndex: 'primary_mac' },
      { title: '标签', dataIndex: 'tags', render: tags => tagsToInline(tags) || '-' },
      { title: '位置', render: (_, r) => `${r.location || '-'} ${r.rack || ''} ${r.rack_unit || ''}` },
      { title: '操作', render: (_, r) => <Space wrap>
        {canWrite && <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)}>编辑</Button>}
        {admin && <Popconfirm title="确认删除该资产？仅无业务引用资产可删除。" onConfirm={() => deleteServer(r)}><Button size="small" danger icon={<DeleteOutlined />}>删除</Button></Popconfirm>}
        {admin && <Button size="small" icon={<ThunderboltOutlined />} disabled={isTerminalServer(r)} onClick={() => configureBmc(r)}>BMC</Button>}
        {canWrite && <Button size="small" icon={<HddOutlined />} onClick={() => openInventory(r)}>硬件</Button>}
        <Button size="small" icon={<DownloadOutlined />} onClick={() => showBOM(r)}>BOM</Button>
        <Button size="small" icon={<SafetyCertificateOutlined />} onClick={() => showFirmware(r)}>固件</Button>
        <Button size="small" onClick={() => downloadBOM(r)}>CSV</Button>
        <Button size="small" onClick={() => showHistory(r)}>状态历史</Button>
        <Button size="small" icon={<LineChartOutlined />} onClick={() => showMonitor(r)}>监控</Button>
        {canWrite && <Button size="small" icon={<StopOutlined />} disabled={isTerminalServer(r)} onClick={() => openTerminalLifecycle(r, 'retire')}>退役</Button>}
        {canWrite && <Button size="small" danger icon={<DeleteOutlined />} disabled={!canScrapServer(r)} onClick={() => openTerminalLifecycle(r, 'scrap')}>报废</Button>}
        <Button size="small" onClick={() => getPower(r)}>电源状态</Button>
        {canWrite && <Button size="small" disabled={isTerminalServer(r)} onClick={() => checkBmc(r)}>检查</Button>}
        {canWrite && <Popconfirm title="确认打开该资产电源？" onConfirm={() => power(r.id, 'power-on')}><Button size="small" disabled={isTerminalServer(r)}>开机</Button></Popconfirm>}
        {canWrite && <Popconfirm title="确认关闭该资产电源？" onConfirm={() => power(r.id, 'power-off')}><Button size="small" danger disabled={isTerminalServer(r)}>关机</Button></Popconfirm>}
        {canWrite && <Popconfirm title="确认重启该资产？" onConfirm={() => power(r.id, 'reboot')}><Button size="small" disabled={isTerminalServer(r)}>重启</Button></Popconfirm>}
      </Space> }
    ]} />

    <Modal title={editing ? '编辑服务器资产' : '新建服务器资产'} open={open} onOk={save} onCancel={() => { setOpen(false); setEditing(null); }} width={760} forceRender>
      <Form form={form} layout="vertical" initialValues={{ status: 'discovered', architecture: 'x86_64' }}>
        <Space.Compact block>
          <Form.Item name="asset_no" label="资产编号" style={{ width: '50%' }}><Input /></Form.Item>
          <Form.Item name="hostname" label="主机名" style={{ width: '50%' }}><Input /></Form.Item>
        </Space.Compact>
        <Space.Compact block>
          <Form.Item name="status" label="状态" style={{ width: '50%' }}><Select options={serverStatuses.map(v => ({ value: v, label: v }))} /></Form.Item>
          <Form.Item name="architecture" label="架构" style={{ width: '50%' }}><Select options={['x86_64', 'arm64'].map(v => ({ value: v, label: v }))} /></Form.Item>
        </Space.Compact>
        <Space.Compact block>
          <Form.Item name="primary_ip" label="IP" style={{ width: '50%' }}><Input /></Form.Item>
          <Form.Item name="primary_mac" label="MAC" style={{ width: '50%' }}><Input /></Form.Item>
        </Space.Compact>
        <Space.Compact block>
          <Form.Item name="serial_number" label="序列号" style={{ width: '50%' }}><Input /></Form.Item>
          <Form.Item name="motherboard_uuid" label="主板 UUID" style={{ width: '50%' }}><Input /></Form.Item>
        </Space.Compact>
        <Space.Compact block>
          <Form.Item name="tenant_id" label="租户" style={{ width: '50%' }}><Select allowClear showSearch optionFilterProp="label" options={tenants.map(tenant => ({ value: tenant.tenant_id, label: `${tenant.tenant_id} · ${tenant.name}${tenant.status === 'disabled' ? ' · disabled' : ''}`, disabled: tenant.status === 'disabled' }))} /></Form.Item>
          <Form.Item name="owner" label="负责人" style={{ width: '50%' }}><Input /></Form.Item>
        </Space.Compact>
        <Space.Compact block>
          <Form.Item name="location" label="机房" style={{ width: '34%' }}><Input /></Form.Item>
          <Form.Item name="rack" label="机柜" style={{ width: '33%' }}><Input /></Form.Item>
          <Form.Item name="rack_unit" label="U 位" style={{ width: '33%' }}><Input /></Form.Item>
        </Space.Compact>
        <Form.Item name="tags_text" label="标签 JSON"><Input.TextArea rows={2} placeholder='["gpu","prod"]' /></Form.Item>
        <Form.Item name="notes" label="备注"><Input.TextArea rows={3} /></Form.Item>
      </Form>
    </Modal>

    <Modal
      title={`${lifecycleActionLabel(terminalAction)}资产 - ${activeServer?.hostname || activeServer?.asset_no || ''}`}
      open={retireOpen}
      onOk={submitTerminalLifecycle}
      onCancel={() => setRetireOpen(false)}
      okText={lifecycleActionLabel(terminalAction)}
      cancelText="取消"
      okButtonProps={{ danger: true }}
      width={640}
      forceRender
    >
      <Form form={retirementForm} layout="vertical" initialValues={{ erase_status: 'pending' }}>
        <Form.Item name="reason" label={`${lifecycleActionLabel(terminalAction)}原因`} rules={[{ required: true, message: `请输入${lifecycleActionLabel(terminalAction)}原因` }, { max: 500 }]}>
          <Input.TextArea rows={3} />
        </Form.Item>
        <Form.Item name="erase_status" label="擦除状态" rules={[{ required: true }]}>
          <Select options={eraseStatusOptions} />
        </Form.Item>
        <Form.Item name="erase_method" label="擦除方式" rules={retireEraseStatus === 'verified' ? [{ required: true, message: '请输入擦除方式' }, { max: 120 }] : [{ max: 120 }]}>
          <Input />
        </Form.Item>
        <Form.Item name="evidence" label="擦除证据" rules={retireEraseStatus === 'verified' ? [{ required: true, message: '请输入擦除证据' }, { max: 2000 }] : [{ max: 2000 }]}>
          <Input.TextArea rows={4} />
        </Form.Item>
      </Form>
    </Modal>

    <Modal title="批量导入资产" open={importOpen} onOk={importServers} onCancel={() => setImportOpen(false)} width={720} forceRender>
      <Form form={importForm} layout="vertical" initialValues={{ payload: '[{"asset_no":"BM-1001","hostname":"node-1001","primary_mac":"52:54:00:00:10:01","architecture":"x86_64","status":"ready","tenant_id":"default"}]' }}>
        <Form.Item name="payload" label="JSON" rules={[{ required: true }]}><Input.TextArea rows={10} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`配置 BMC - ${activeServer?.hostname || ''}`} open={bmcOpen} onOk={saveBmc} onCancel={() => setBmcOpen(false)} forceRender>
      <Form form={bmcForm} layout="vertical">
        <Form.Item name="type" label="类型"><Select options={['redfish', 'ipmi'].map(v => ({ value: v, label: v.toUpperCase() }))} /></Form.Item>
        <Form.Item name="protocol" label="协议"><Select options={['https', 'http', 'ipmi'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="endpoint" label="端点" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="username" label="用户名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="password" label="密码"><Input.Password placeholder="留空则保留已有凭据" /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`记录硬件信息 - ${activeServer?.hostname || ''}`} open={inventoryOpen} onOk={saveInventory} onCancel={() => setInventoryOpen(false)} forceRender>
      <Form form={inventoryForm} layout="vertical">
        <Form.Item name="cpu_summary" label="CPU"><Input /></Form.Item>
        <Form.Item name="memory_summary" label="内存"><Input /></Form.Item>
        <Form.Item name="disk_summary" label="磁盘"><Input /></Form.Item>
        <Form.Item name="network_summary" label="网卡"><Input /></Form.Item>
        <Form.Item name="gpu_summary" label="GPU"><Input /></Form.Item>
        <Form.Item name="raid_summary" label="RAID/HBA"><Input /></Form.Item>
        <Form.Item name="collected_by" label="采集来源"><Input /></Form.Item>
      </Form>
    </Modal>

    <Modal title={`BOM - ${activeServer?.hostname || ''}`} open={bomOpen} onCancel={() => setBomOpen(false)} footer={<Space><Button icon={<DownloadOutlined />} onClick={() => activeServer && downloadBOM(activeServer)}>下载 CSV</Button><Button type="primary" onClick={() => setBomOpen(false)}>关闭</Button></Space>}>
      <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label="资产编号">{bom?.asset_no || '-'}</Descriptions.Item>
        <Descriptions.Item label="主机名">{bom?.hostname || '-'}</Descriptions.Item>
        <Descriptions.Item label="CPU">{bom?.cpu_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="内存">{bom?.memory_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="磁盘">{bom?.disk_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="网卡">{bom?.network_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="GPU">{bom?.gpu_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="RAID/HBA">{bom?.raid_summary || '-'}</Descriptions.Item>
        <Descriptions.Item label="采集来源">{bom?.collected_by || '-'}</Descriptions.Item>
      </Descriptions>
    </Modal>

    <Modal title={`BMC 固件 - ${activeServer?.hostname || ''}`} open={firmwareOpen} onCancel={() => setFirmwareOpen(false)} footer={<Button type="primary" onClick={() => setFirmwareOpen(false)}>关闭</Button>} width={620}>
      {firmwareLoading ? <Spin /> : <Descriptions column={1} size="small" bordered>
        <Descriptions.Item label="适配器">{firmware?.adapter || '-'}</Descriptions.Item>
        <Descriptions.Item label="端点状态">{firmware?.endpoint_status || '-'}</Descriptions.Item>
        <Descriptions.Item label="厂商">{firmware?.manufacturer || '-'}</Descriptions.Item>
        <Descriptions.Item label="型号">{firmware?.model || '-'}</Descriptions.Item>
        <Descriptions.Item label="序列号">{firmware?.serial_number || '-'}</Descriptions.Item>
        <Descriptions.Item label="固件版本">{firmware?.firmware_version || '-'}</Descriptions.Item>
        <Descriptions.Item label="BIOS 版本">{firmware?.bios_version || '-'}</Descriptions.Item>
        <Descriptions.Item label="BMC 版本">{firmware?.bmc_version || '-'}</Descriptions.Item>
        <Descriptions.Item label="最近检查">{firmware?.last_checked_at || '-'}</Descriptions.Item>
      </Descriptions>}
    </Modal>

    <Modal title={`状态历史 - ${activeServer?.hostname || ''}`} open={historyOpen} footer={null} onCancel={() => setHistoryOpen(false)} width={920}>
      <Tabs items={[
        { key: 'history', label: '状态历史', children: <Table rowKey="id" size="small" pagination={false} dataSource={history} columns={[
          { title: '原状态', dataIndex: 'from_status', render: (v) => v || '-' },
          { title: '新状态', dataIndex: 'to_status', render: (v) => <Tag>{v}</Tag> },
          { title: '原因', dataIndex: 'reason' },
          { title: '操作者', dataIndex: 'actor_email' },
          { title: '时间', dataIndex: 'created_at' }
        ]} /> },
        { key: 'retirement', label: '退役/报废记录', children: <Table rowKey="id" size="small" pagination={false} dataSource={retirementRecords} columns={[
          { title: '终态', dataIndex: 'to_status', render: (v) => <Tag>{v}</Tag> },
          { title: '原因', dataIndex: 'reason' },
          { title: '擦除状态', dataIndex: 'erase_status', render: (v) => <Tag color={eraseStatusColor(v)}>{v}</Tag> },
          { title: '擦除方式', dataIndex: 'erase_method', render: (v) => v || '-' },
          { title: '擦除证据', dataIndex: 'evidence', ellipsis: true, render: (v) => v || '-' },
          { title: '操作者', dataIndex: 'requested_by' },
          { title: '时间', dataIndex: 'requested_at' }
        ]} /> }
      ]} />
    </Modal>

    <Modal title={`监控采集 - ${activeServer?.hostname || ''}`} open={monitorOpen} footer={null} onCancel={() => setMonitorOpen(false)} width={860}>
      <div className="toolbar">
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={refreshMonitor}>刷新</Button>
          {canWrite && <Button type="primary" icon={<ToolOutlined />} disabled={isTerminalServer(activeServer)} onClick={startCollection}>触发采集</Button>}
          {admin && <Button icon={<ToolOutlined />} disabled={isTerminalServer(activeServer)} onClick={() => activeServer && openSSH(activeServer)}>SSH 配置</Button>}
        </Space>
      </div>
      <Tabs items={[
        { key: 'metrics', label: '最新指标', children: <Table rowKey={(r) => `${r.metric_name}-${r.collected_at}`} size="small" pagination={false} dataSource={metrics} columns={[
          { title: '指标', dataIndex: 'metric_name' },
          { title: '数值', render: (_, r) => `${r.value}${r.unit || ''}` },
          { title: '采集时间', dataIndex: 'collected_at' }
        ]} /> },
        { key: 'collections', label: '采集历史', children: <Table rowKey="id" size="small" pagination={false} dataSource={collections} columns={[
          { title: 'ID', dataIndex: 'id' },
          { title: '模式', dataIndex: 'mode' },
          { title: '状态', dataIndex: 'status', render: (v) => <Tag>{v}</Tag> },
          { title: '发起人', dataIndex: 'requested_by' },
          { title: '开始时间', dataIndex: 'started_at' },
          { title: '结束时间', dataIndex: 'finished_at' },
          { title: '错误', dataIndex: 'error_message' }
        ]} /> }
      ]} />
    </Modal>

    <Modal title={`SSH 采集配置 - ${activeServer?.hostname || ''}`} open={sshOpen} onOk={saveSSH} onCancel={() => setSshOpen(false)} width={620} forceRender>
      <Form form={sshForm} layout="vertical">
        <Form.Item name="host" label="SSH Host" rules={[{ required: true }]}><Input placeholder="192.168.100.21" /></Form.Item>
        <Form.Item name="port" label="SSH Port"><InputNumber min={1} max={65535} style={{ width: '100%' }} placeholder="22" /></Form.Item>
        <Form.Item name="username" label="用户名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="auth_type" label="认证方式"><Select options={[{ value: 'password', label: 'password' }, { value: 'private_key', label: 'private_key' }]} /></Form.Item>
        <Form.Item name="secret" label="密码或私钥"><Input.Password placeholder="留空则保留已有加密凭据" /></Form.Item>
      </Form>
    </Modal>
  </>;
}
