import { EditOutlined, LockOutlined, PlusOutlined, ReloadOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, ConfigIssue, NetworkCheck, NetworkCheckReport, NetworkConfig, PageResult, ReadinessCheck, ReadinessStatus, rootApi, Tenant, User } from '../api/client';

const roleColor = (role: string) => role === 'admin' ? 'red' : role === 'operator' ? 'blue' : 'default';
const readinessColor = (status: string) => status === 'ok' ? 'green' : status === 'warning' ? 'orange' : 'red';
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

  useEffect(() => { void loadUsers(1, pageSize); void loadTenants(1, tenantPageSize); void loadNetworks(1, networkPageSize); void loadReadiness(); }, []);

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
          { title: '状态', dataIndex: 'status', render: value => <Tag color={readinessColor(value)}>{value}</Tag> },
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
