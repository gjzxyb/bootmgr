import { CodeOutlined, DeleteOutlined, PlusOutlined, ReloadOutlined, ToolOutlined } from '@ant-design/icons';
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, apiErrorMessage, InstallTemplate, PageResult, WorkflowTemplate } from '../api/client';
import { canManage, isAdmin } from '../authz';

const statusColor = (status: string) => status === 'enabled' ? 'green' : 'default';
const osFamilyOptions = ['ubuntu', 'rocky', 'debian', 'openEuler', 'kylin', 'uos', 'windows'].map(value => ({ value, label: value }));
const templateTypeOptions = ['cloud-init', 'autoinstall', 'kickstart', 'preseed', 'unattend'].map(value => ({ value, label: value }));
const statusOptions = ['enabled', 'disabled'].map(value => ({ value, label: value }));
const jsonToText = (value: unknown) => {
  if (value === undefined || value === null || value === '') return '';
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
};

export function TemplatesPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const admin = isAdmin(role);
  const [installRows, setInstallRows] = useState<InstallTemplate[]>([]);
  const [workflowRows, setWorkflowRows] = useState<WorkflowTemplate[]>([]);
  const [installTotal, setInstallTotal] = useState(0);
  const [workflowTotal, setWorkflowTotal] = useState(0);
  const [installPage, setInstallPage] = useState(1);
  const [workflowPage, setWorkflowPage] = useState(1);
  const [installPageSize, setInstallPageSize] = useState(20);
  const [workflowPageSize, setWorkflowPageSize] = useState(20);
  const [installOpen, setInstallOpen] = useState(false);
  const [workflowOpen, setWorkflowOpen] = useState(false);
  const [installSaving, setInstallSaving] = useState(false);
  const [workflowSaving, setWorkflowSaving] = useState(false);
  const [editingInstall, setEditingInstall] = useState<InstallTemplate | null>(null);
  const [editingWorkflow, setEditingWorkflow] = useState<WorkflowTemplate | null>(null);
  const [installForm] = Form.useForm();
  const [workflowForm] = Form.useForm();
  const [installFilterForm] = Form.useForm();
  const [workflowFilterForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const loadInstall = async (nextPage = installPage, nextPageSize = installPageSize) => {
    const values = installFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<InstallTemplate>>('/install-templates', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setInstallRows(data.items);
    setInstallTotal(data.total);
    setInstallPage(data.page);
    setInstallPageSize(data.page_size);
  };

  const loadWorkflow = async (nextPage = workflowPage, nextPageSize = workflowPageSize) => {
    const values = workflowFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<WorkflowTemplate>>('/workflow-templates', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setWorkflowRows(data.items);
    setWorkflowTotal(data.total);
    setWorkflowPage(data.page);
    setWorkflowPageSize(data.page_size);
  };

  useEffect(() => { void loadInstall(1, installPageSize); void loadWorkflow(1, workflowPageSize); }, []);

  const openInstall = (row?: InstallTemplate) => {
    setEditingInstall(row || null);
    installForm.resetFields();
    installForm.setFieldsValue(row ? { ...row, variables_schema_text: jsonToText(row.variables_schema) } : {
      status: 'enabled',
      template_type: 'cloud-init',
      os_family: 'ubuntu',
      version: 'v1',
      content: '#cloud-config\nhostname: {{hostname}}\n',
      variables_schema_text: '{\n  "hostname": {\n    "type": "string"\n  }\n}'
    });
    setInstallOpen(true);
  };

  const saveInstall = async () => {
    if (installSaving) return;
    const values = await installForm.validateFields();
    const payload = { ...values };
    const schemaText = typeof payload.variables_schema_text === 'string' ? payload.variables_schema_text.trim() : '';
    if (schemaText) {
      try {
        const parsed = JSON.parse(schemaText);
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          msg.error('变量 Schema 必须是 JSON object');
          return;
        }
        payload.variables_schema = parsed;
      } catch {
        msg.error('变量 Schema JSON 无效');
        return;
      }
    } else {
      payload.variables_schema = null;
    }
    delete payload.variables_schema_text;
    setInstallSaving(true);
    try {
      if (editingInstall) await api.patch(`/install-templates/${editingInstall.id}`, payload, { suppressGlobalError: true });
      else await api.post('/install-templates', payload, { suppressGlobalError: true });
      msg.success('安装模板已保存');
      setInstallOpen(false);
      void loadInstall(1, installPageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '安装模板保存失败'));
    } finally {
      setInstallSaving(false);
    }
  };

  const toggleInstall = async (row: InstallTemplate) => {
    await api.patch(`/install-templates/${row.id}`, { status: row.status === 'enabled' ? 'disabled' : 'enabled' });
    msg.success('安装模板状态已更新');
    void loadInstall(installPage, installPageSize);
  };

  const deleteInstall = async (row: InstallTemplate) => {
    await api.delete(`/install-templates/${row.id}`, { headers: { 'X-Confirm-Action': 'install_template.delete' } });
    msg.success('安装模板已删除');
    void loadInstall(installPage, installPageSize);
  };

  const openWorkflow = (row?: WorkflowTemplate) => {
    setEditingWorkflow(row || null);
    workflowForm.resetFields();
    const definition = row?.definition ? JSON.stringify(row.definition, null, 2) : '{\n  "steps": [\n    { "name": "Render iPXE", "action": "render_ipxe" },\n    { "name": "Install OS", "action": "install_os" }\n  ]\n}';
    workflowForm.setFieldsValue(row ? { ...row, definition } : { status: 'enabled', version: 'v1', definition });
    setWorkflowOpen(true);
  };

  const saveWorkflow = async () => {
    if (workflowSaving) return;
    const values = await workflowForm.validateFields();
    try { values.definition = JSON.parse(values.definition); } catch { msg.error('工作流定义 JSON 无效'); return; }
    setWorkflowSaving(true);
    try {
      if (editingWorkflow) await api.patch(`/workflow-templates/${editingWorkflow.id}`, values, { suppressGlobalError: true });
      else await api.post('/workflow-templates', values, { suppressGlobalError: true });
      msg.success('工作流模板已保存');
      setWorkflowOpen(false);
      void loadWorkflow(1, workflowPageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '工作流模板保存失败'));
    } finally {
      setWorkflowSaving(false);
    }
  };

  const toggleWorkflow = async (row: WorkflowTemplate) => {
    await api.patch(`/workflow-templates/${row.id}`, { status: row.status === 'enabled' ? 'disabled' : 'enabled' });
    msg.success('工作流模板状态已更新');
    void loadWorkflow(workflowPage, workflowPageSize);
  };

  const deleteWorkflow = async (row: WorkflowTemplate) => {
    await api.delete(`/workflow-templates/${row.id}`, { headers: { 'X-Confirm-Action': 'workflow_template.delete' } });
    msg.success('工作流模板已删除');
    void loadWorkflow(workflowPage, workflowPageSize);
  };

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">模板管理</Typography.Title>
    <Tabs items={[
      { key: 'install', label: '安装模板', children: <>
        <div className="toolbar">
          <Space>{canWrite && <Button type="primary" icon={<PlusOutlined />} onClick={() => openInstall()}>新建安装模板</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadInstall(installPage, installPageSize)}>刷新</Button></Space>
          <Form form={installFilterForm} layout="inline" onFinish={() => loadInstall(1, installPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="名称/内容" allowClear /></Form.Item>
            <Form.Item name="os_family"><Select placeholder="系统族" allowClear style={{ width: 140 }} options={osFamilyOptions} /></Form.Item>
            <Form.Item name="template_type"><Select placeholder="模板类型" allowClear style={{ width: 150 }} options={templateTypeOptions} /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={statusOptions} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { installFilterForm.resetFields(); void loadInstall(1, installPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={installRows} pagination={{ current: installPage, pageSize: installPageSize, total: installTotal, showSizeChanger: true, onChange: (p, ps) => loadInstall(p, ps) }} columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '系统', render: (_, r) => `${r.os_family || '-'} ${r.os_version || ''}` },
          { title: '类型', dataIndex: 'template_type' },
          { title: '版本', dataIndex: 'version' },
          { title: '变量', render: (_, r) => r.variables_schema ? <Tag color="blue">schema</Tag> : '-' },
          { title: '状态', dataIndex: 'status', render: (v) => <Tag color={statusColor(v)}>{v}</Tag> },
          { title: '操作', render: (_, r) => canWrite ? <Space><Button size="small" icon={<CodeOutlined />} onClick={() => openInstall(r)}>编辑</Button><Button size="small" onClick={() => toggleInstall(r)}>{r.status === 'enabled' ? '禁用' : '启用'}</Button>{admin && <Popconfirm title="确认删除该安装模板？" onConfirm={() => deleteInstall(r)}><Button size="small" danger icon={<DeleteOutlined />}>删除</Button></Popconfirm>}</Space> : '-' }
        ]} />
      </> },
      { key: 'workflow', label: '工作流模板', forceRender: true, children: <>
        <div className="toolbar">
          <Space>{canWrite && <Button type="primary" icon={<PlusOutlined />} onClick={() => openWorkflow()}>新建工作流模板</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadWorkflow(workflowPage, workflowPageSize)}>刷新</Button></Space>
          <Form form={workflowFilterForm} layout="inline" onFinish={() => loadWorkflow(1, workflowPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="名称/描述" allowClear /></Form.Item>
            <Form.Item name="version"><Input placeholder="版本" allowClear /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={statusOptions} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { workflowFilterForm.resetFields(); void loadWorkflow(1, workflowPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={workflowRows} pagination={{ current: workflowPage, pageSize: workflowPageSize, total: workflowTotal, showSizeChanger: true, onChange: (p, ps) => loadWorkflow(p, ps) }} columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '版本', dataIndex: 'version' },
          { title: '描述', dataIndex: 'description' },
          { title: '状态', dataIndex: 'status', render: (v) => <Tag color={statusColor(v)}>{v}</Tag> },
          { title: '操作', render: (_, r) => canWrite ? <Space><Button size="small" icon={<ToolOutlined />} onClick={() => openWorkflow(r)}>编辑</Button><Button size="small" onClick={() => toggleWorkflow(r)}>{r.status === 'enabled' ? '禁用' : '启用'}</Button>{admin && <Popconfirm title="确认删除该工作流模板？" onConfirm={() => deleteWorkflow(r)}><Button size="small" danger icon={<DeleteOutlined />}>删除</Button></Popconfirm>}</Space> : '-' }
        ]} />
      </> }
    ]} />

    <Modal title={editingInstall ? '编辑安装模板' : '新建安装模板'} open={installOpen} onOk={saveInstall} confirmLoading={installSaving} onCancel={() => setInstallOpen(false)} width={820} forceRender>
      <Form form={installForm} layout="vertical">
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Space.Compact block><Form.Item name="os_family" label="系统族" style={{ width: '50%' }}><Select options={osFamilyOptions} /></Form.Item><Form.Item name="os_version" label="系统版本" style={{ width: '50%' }}><Input /></Form.Item></Space.Compact>
        <Space.Compact block><Form.Item name="template_type" label="模板类型" style={{ width: '50%' }}><Select options={templateTypeOptions} /></Form.Item><Form.Item name="version" label="版本" style={{ width: '50%' }}><Input /></Form.Item></Space.Compact>
        <Form.Item name="status" label="状态"><Select options={statusOptions} /></Form.Item>
        <Form.Item name="variables_schema_text" label="变量 Schema JSON"><Input.TextArea rows={5} /></Form.Item>
        <Form.Item name="content" label="模板内容"><Input.TextArea rows={10} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={editingWorkflow ? '编辑工作流模板' : '新建工作流模板'} open={workflowOpen} onOk={saveWorkflow} confirmLoading={workflowSaving} onCancel={() => setWorkflowOpen(false)} width={820} forceRender>
      <Form form={workflowForm} layout="vertical">
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="version" label="版本"><Input /></Form.Item>
        <Form.Item name="description" label="描述"><Input /></Form.Item>
        <Form.Item name="status" label="状态"><Select options={statusOptions} /></Form.Item>
        <Form.Item name="definition" label="工作流定义 JSON"><Input.TextArea rows={12} /></Form.Item>
      </Form>
    </Modal>
  </>;
}
