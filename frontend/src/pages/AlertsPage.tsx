import { CheckOutlined, EditOutlined, HistoryOutlined, ReloadOutlined, ToolOutlined } from '@ant-design/icons';
import { Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useState } from 'react';
import { Alert, AlertEvent, AlertRule, CollectionJob, LogEvent, PageResult, api, apiErrorMessage } from '../api/client';
import { canManage, isAdmin } from '../authz';

const severityColor = (severity: string) => severity === 'critical' ? 'red' : severity === 'warning' ? 'orange' : 'blue';
const statusColor = (status: string) => status === 'resolved' || status === 'success' ? 'green' : status === 'acknowledged' || status === 'running' ? 'blue' : status === 'failed' || status === 'firing' ? 'red' : 'default';
const levelColor = (level: string) => level === 'error' ? 'red' : level === 'warning' ? 'orange' : level === 'info' ? 'blue' : 'default';

export function AlertsPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const admin = isAdmin(role);
  const [rows, setRows] = useState<Alert[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [jobs, setJobs] = useState<CollectionJob[]>([]);
  const [logEvents, setLogEvents] = useState<LogEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [ruleTotal, setRuleTotal] = useState(0);
  const [jobTotal, setJobTotal] = useState(0);
  const [logTotal, setLogTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [rulePage, setRulePage] = useState(1);
  const [jobPage, setJobPage] = useState(1);
  const [logPage, setLogPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [rulePageSize, setRulePageSize] = useState(20);
  const [jobPageSize, setJobPageSize] = useState(20);
  const [logPageSize, setLogPageSize] = useState(20);
  const [ruleOpen, setRuleOpen] = useState(false);
  const [collectOpen, setCollectOpen] = useState(false);
  const [handleOpen, setHandleOpen] = useState(false);
  const [eventsOpen, setEventsOpen] = useState(false);
  const [current, setCurrent] = useState<Alert | null>(null);
  const [editingRule, setEditingRule] = useState<AlertRule | null>(null);
  const [handleAction, setHandleAction] = useState<'ack' | 'resolve'>('ack');
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [collectSubmitting, setCollectSubmitting] = useState(false);
  const [ruleSaving, setRuleSaving] = useState(false);
  const [handleSubmitting, setHandleSubmitting] = useState(false);
  const [evaluatingRules, setEvaluatingRules] = useState(false);
  const [filterForm] = Form.useForm();
  const [ruleFilterForm] = Form.useForm();
  const [ruleForm] = Form.useForm();
  const [jobFilterForm] = Form.useForm();
  const [logFilterForm] = Form.useForm();
  const [collectForm] = Form.useForm();
  const [handleForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const load = async (nextPage = page, nextPageSize = pageSize, filters?: Record<string, unknown>) => {
    const values = filters ?? filterForm.getFieldsValue();
    const { data } = await api.get<PageResult<Alert>>('/alerts', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRows(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };

  const loadRules = async (nextPage = rulePage, nextPageSize = rulePageSize, filters?: Record<string, unknown>) => {
    const values = filters ?? ruleFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<AlertRule>>('/alert-rules', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRules(data.items);
    setRuleTotal(data.total);
    setRulePage(data.page);
    setRulePageSize(data.page_size);
  };

  const loadJobs = async (nextPage = jobPage, nextPageSize = jobPageSize, filters?: Record<string, unknown>) => {
    const values = filters ?? jobFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<CollectionJob>>('/collections', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setJobs(data.items);
    setJobTotal(data.total);
    setJobPage(data.page);
    setJobPageSize(data.page_size);
  };

  const loadLogEvents = async (nextPage = logPage, nextPageSize = logPageSize, filters?: Record<string, unknown>) => {
    const values = filters ?? logFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<LogEvent>>('/log-events', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setLogEvents(data.items);
    setLogTotal(data.total);
    setLogPage(data.page);
    setLogPageSize(data.page_size);
  };

  useEffect(() => { void load(1, pageSize, {}); void loadRules(1, rulePageSize, {}); void loadJobs(1, jobPageSize, {}); void loadLogEvents(1, logPageSize, {}); }, []);

  const openCreateRule = () => {
    setEditingRule(null);
    ruleForm.resetFields();
    ruleForm.setFieldsValue({ operator: '>', severity: 'warning', status: 'enabled' });
    setRuleOpen(true);
  };

  const openEditRule = (rule: AlertRule) => {
    setEditingRule(rule);
    ruleForm.resetFields();
    ruleForm.setFieldsValue({
      rule_id: rule.rule_id,
      name: rule.name,
      description: rule.description,
      metric_name: rule.metric_name,
      operator: rule.operator,
      threshold: rule.threshold,
      severity: rule.severity,
      status: rule.status
    });
    setRuleOpen(true);
  };

  const closeRuleModal = () => {
    setRuleOpen(false);
    setEditingRule(null);
    ruleForm.resetFields();
  };

  const saveRule = async () => {
    if (ruleSaving) return;
    const values = await ruleForm.validateFields();
    setRuleSaving(true);
    try {
      if (editingRule) {
        await api.patch(`/alert-rules/${editingRule.id}`, values, { suppressGlobalError: true });
        msg.success('告警规则已更新');
        closeRuleModal();
        void loadRules(rulePage, rulePageSize);
      } else {
        await api.post('/alert-rules', values, { suppressGlobalError: true });
        msg.success('告警规则已创建');
        closeRuleModal();
        void loadRules(1, rulePageSize);
      }
    } catch (error) {
      msg.error(apiErrorMessage(error, '告警规则保存失败'));
    } finally {
      setRuleSaving(false);
    }
  };

  const toggleRule = async (rule: AlertRule) => {
    const status = rule.status === 'enabled' ? 'disabled' : 'enabled';
    await api.patch(`/alert-rules/${rule.id}`, { status });
    msg.success(status === 'enabled' ? '规则已启用' : '规则已禁用');
    void loadRules(rulePage, rulePageSize);
  };

  const evaluateRules = async () => {
    if (evaluatingRules) return;
    setEvaluatingRules(true);
    try {
      const { data } = await api.post('/alert-rules/evaluate', {}, { suppressGlobalError: true });
      msg.success(`规则评估完成，新增 ${data.created || 0} 条，去重 ${data.deduplicated || 0} 条`);
      void load(1, pageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '规则评估失败'));
    } finally {
      setEvaluatingRules(false);
    }
  };

  const openHandle = (alert: Alert, action: 'ack' | 'resolve') => {
    setCurrent(alert);
    setHandleAction(action);
    handleForm.resetFields();
    handleForm.setFieldsValue({ note: action === 'ack' ? '开始排查' : '故障已处理' });
    setHandleOpen(true);
  };

  const submitHandle = async () => {
    if (!current || handleSubmitting) return;
    const values = await handleForm.validateFields();
    setHandleSubmitting(true);
    try {
      await api.post(`/alerts/${current.id}/${handleAction}`, values, { suppressGlobalError: true });
      msg.success(handleAction === 'ack' ? '告警已确认' : '告警已关闭');
      setHandleOpen(false);
      handleForm.resetFields();
      void load(page, pageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, handleAction === 'ack' ? '告警确认失败' : '告警关闭失败'));
    } finally {
      setHandleSubmitting(false);
    }
  };

  const showEvents = async (alert: Alert) => {
    const { data } = await api.get(`/alerts/${alert.id}/events`);
    setEvents(data);
    setEventsOpen(true);
  };

  const openCollect = () => {
    collectForm.resetFields();
    collectForm.setFieldsValue({ auth_type: 'password' });
    setCollectOpen(true);
  };

  const collect = async () => {
    if (collectSubmitting) return;
    const values = await collectForm.validateFields();
    const { server_id, host, username, secret, port, auth_type } = values;
    setCollectSubmitting(true);
    try {
      if (host && username) {
        if (!admin) {
          msg.error('只有管理员可以保存 SSH 凭据');
          return;
        }
        await api.post(`/servers/${server_id}/ssh`, { host, username, secret, port: port || 22, auth_type: auth_type || 'password' }, { headers: { 'X-Confirm-Action': 'ssh.upsert' }, suppressGlobalError: true });
      }
      await api.post(`/servers/${server_id}/collections`, {}, { suppressGlobalError: true });
      msg.success('Agentless 采集任务已启动');
      setCollectOpen(false);
      collectForm.resetFields();
      void loadJobs(1, jobPageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, 'Agentless 采集任务启动失败'));
    } finally {
      setCollectSubmitting(false);
    }
  };

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">监控告警</Typography.Title>
    <Tabs items={[
      { key: 'alerts', label: '告警列表', children: <>
        <div className="toolbar">
          <Space>{canWrite && <Button type="primary" icon={<ToolOutlined />} onClick={openCollect}>触发采集</Button>}<Button icon={<ReloadOutlined />} onClick={() => load(page, pageSize)}>刷新</Button></Space>
          <Form form={filterForm} layout="inline" onFinish={() => load(1, pageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="标题/描述" allowClear /></Form.Item>
            <Form.Item name="server_id"><Input placeholder="服务器 ID" allowClear /></Form.Item>
            <Form.Item name="severity"><Select placeholder="级别" allowClear style={{ width: 130 }} options={['critical', 'warning', 'info'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 150 }} options={['firing', 'acknowledged', 'resolved'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="rule_id"><Input placeholder="规则 ID" allowClear /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { filterForm.resetFields(); void load(1, pageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={rows} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => load(p, ps) }} columns={[
          { title: '级别', dataIndex: 'severity', render: v => <Tag color={severityColor(v)}>{v}</Tag> },
          { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
          { title: '服务器', dataIndex: 'server_id' },
          { title: '标题', dataIndex: 'title' },
          { title: '描述', dataIndex: 'description' },
          { title: '确认人', dataIndex: 'acknowledged_by' },
          { title: '关闭人', dataIndex: 'resolved_by' },
          { title: '触发时间', dataIndex: 'triggered_at' },
          { title: '操作', render: (_, r) => <Space><Button size="small" icon={<CheckOutlined />} disabled={r.status === 'resolved'} onClick={() => openHandle(r, 'ack')}>确认</Button><Button size="small" danger disabled={r.status === 'resolved'} onClick={() => openHandle(r, 'resolve')}>关闭</Button><Button size="small" icon={<HistoryOutlined />} onClick={() => showEvents(r)}>记录</Button></Space> }
        ]} />
      </> },
      { key: 'rules', label: '告警规则', forceRender: true, children: <>
        <div className="toolbar">
          <Space>{canWrite && <Button type="primary" onClick={openCreateRule}>新建规则</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadRules(rulePage, rulePageSize)}>刷新</Button>{canWrite && <Button onClick={evaluateRules} loading={evaluatingRules}>评估规则</Button>}</Space>
          <Form form={ruleFilterForm} layout="inline" onFinish={() => loadRules(1, rulePageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="规则/名称/描述" allowClear /></Form.Item>
            <Form.Item name="metric_name"><Input placeholder="指标名" allowClear /></Form.Item>
            <Form.Item name="severity"><Select placeholder="级别" allowClear style={{ width: 130 }} options={['critical', 'warning', 'info'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { ruleFilterForm.resetFields(); void loadRules(1, rulePageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={rules} pagination={{ current: rulePage, pageSize: rulePageSize, total: ruleTotal, showSizeChanger: true, onChange: (p, ps) => loadRules(p, ps) }} columns={[
          { title: '规则 ID', dataIndex: 'rule_id' },
          { title: '名称', dataIndex: 'name' },
          { title: '指标', dataIndex: 'metric_name' },
          { title: '条件', render: (_, r) => `${r.metric_name} ${r.operator} ${r.threshold}` },
          { title: '级别', dataIndex: 'severity', render: v => <Tag color={severityColor(v)}>{v}</Tag> },
          { title: '状态', dataIndex: 'status', render: v => <Tag color={v === 'enabled' ? 'green' : 'default'}>{v}</Tag> },
          { title: '操作', render: (_, r) => canWrite ? <Space><Button size="small" icon={<EditOutlined />} onClick={() => openEditRule(r)}>编辑</Button><Button size="small" onClick={() => toggleRule(r)}>{r.status === 'enabled' ? '禁用' : '启用'}</Button></Space> : '-' }
        ]} />
      </> },
      { key: 'collections', label: '采集任务', forceRender: true, children: <>
        <div className="toolbar">
          <Space>{canWrite && <Button type="primary" icon={<ToolOutlined />} onClick={openCollect}>触发采集</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadJobs(jobPage, jobPageSize)}>刷新</Button></Space>
          <Form form={jobFilterForm} layout="inline" onFinish={() => loadJobs(1, jobPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="server_id"><Input placeholder="服务器 ID" allowClear /></Form.Item>
            <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={['running', 'success', 'failed'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="mode"><Select placeholder="模式" allowClear style={{ width: 160 }} options={[{ value: 'ssh_agentless', label: 'ssh_agentless' }]} /></Form.Item>
            <Form.Item name="requested_by"><Input placeholder="发起人" allowClear /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { jobFilterForm.resetFields(); void loadJobs(1, jobPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={jobs} pagination={{ current: jobPage, pageSize: jobPageSize, total: jobTotal, showSizeChanger: true, onChange: (p, ps) => loadJobs(p, ps) }} columns={[
          { title: 'ID', dataIndex: 'id' },
          { title: '服务器', dataIndex: 'server_id' },
          { title: '模式', dataIndex: 'mode' },
          { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
          { title: '发起人', dataIndex: 'requested_by' },
          { title: '开始时间', dataIndex: 'started_at' },
          { title: '结束时间', dataIndex: 'finished_at' },
          { title: '错误', dataIndex: 'error_message' }
        ]} />
      </> },
      { key: 'logs', label: '日志事件', forceRender: true, children: <>
        <div className="toolbar">
          <Space><Button icon={<ReloadOutlined />} onClick={() => loadLogEvents(logPage, logPageSize)}>刷新</Button></Space>
          <Form form={logFilterForm} layout="inline" onFinish={() => loadLogEvents(1, logPageSize)} style={{ marginTop: 12 }}>
            <Form.Item name="keyword"><Input placeholder="消息/Trace ID" allowClear /></Form.Item>
            <Form.Item name="server_id"><Input placeholder="服务器 ID" allowClear /></Form.Item>
            <Form.Item name="source"><Select placeholder="来源" allowClear style={{ width: 140 }} options={['agentless', 'workflow', 'bmc', 'system'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Form.Item name="level"><Select placeholder="级别" allowClear style={{ width: 130 }} options={['info', 'warning', 'error'].map(v => ({ value: v, label: v }))} /></Form.Item>
            <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { logFilterForm.resetFields(); void loadLogEvents(1, logPageSize); }}>重置</Button></Space>
          </Form>
        </div>
        <Table rowKey="id" dataSource={logEvents} pagination={{ current: logPage, pageSize: logPageSize, total: logTotal, showSizeChanger: true, onChange: (p, ps) => loadLogEvents(p, ps) }} columns={[
          { title: '级别', dataIndex: 'level', render: v => <Tag color={levelColor(v)}>{v}</Tag> },
          { title: '来源', dataIndex: 'source' },
          { title: '服务器', dataIndex: 'server_id' },
          { title: '消息', dataIndex: 'message' },
          { title: 'Trace ID', dataIndex: 'trace_id' },
          { title: '发生时间', dataIndex: 'occurred_at' }
        ]} />
      </> }
    ]} />

    <Modal title="触发 SSH Agentless 采集" open={collectOpen} onOk={collect} confirmLoading={collectSubmitting} onCancel={() => setCollectOpen(false)} forceRender>
      <Form form={collectForm} layout="vertical" initialValues={{ auth_type: 'password' }}>
        <Form.Item name="server_id" label="服务器 ID" rules={[{ required: true }]}><InputNumber min={1} style={{ width: '100%' }} /></Form.Item>
        {admin && <Form.Item name="host" label="SSH Host"><Input placeholder="留空则使用模拟采集" /></Form.Item>}
        {admin && <Form.Item name="port" label="SSH Port"><InputNumber min={1} max={65535} style={{ width: '100%' }} placeholder="22" /></Form.Item>}
        {admin && <Form.Item name="username" label="SSH 用户名"><Input /></Form.Item>}
        {admin && <Form.Item name="auth_type" label="认证方式"><Select options={[{ value: 'password', label: 'password' }, { value: 'private_key', label: 'private_key' }]} /></Form.Item>}
        {admin && <Form.Item name="secret" label="SSH Secret"><Input.Password placeholder="保存为加密凭据" /></Form.Item>}
      </Form>
    </Modal>

    <Modal title={editingRule ? `编辑告警规则 - ${editingRule.rule_id}` : '新建告警规则'} open={ruleOpen} onOk={saveRule} confirmLoading={ruleSaving} onCancel={closeRuleModal} width={680} forceRender>
      <Form form={ruleForm} layout="vertical">
        <Form.Item name="rule_id" label="规则 ID" rules={[{ required: true }]}><Input placeholder="cpu.high" /></Form.Item>
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="description" label="描述"><Input.TextArea rows={3} /></Form.Item>
        <Form.Item name="metric_name" label="指标名" rules={[{ required: true }]}><Input placeholder="cpu_usage" /></Form.Item>
        <Space.Compact block>
          <Form.Item name="operator" label="操作符" style={{ width: '33%' }} rules={[{ required: true }]}><Select options={['>', '>=', '<', '<=', '=='].map(v => ({ value: v, label: v }))} /></Form.Item>
          <Form.Item name="threshold" label="阈值" style={{ width: '34%' }} rules={[{ required: true }]}><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="severity" label="级别" style={{ width: '33%' }} rules={[{ required: true }]}><Select options={['critical', 'warning', 'info'].map(v => ({ value: v, label: v }))} /></Form.Item>
        </Space.Compact>
        <Form.Item name="status" label="状态"><Select options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
      </Form>
    </Modal>

    <Modal title={handleAction === 'ack' ? '确认告警' : '关闭告警'} open={handleOpen} onOk={submitHandle} confirmLoading={handleSubmitting} onCancel={() => setHandleOpen(false)} forceRender>
      <Form form={handleForm} layout="vertical">
        <Form.Item name="note" label="处理说明" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
      </Form>
    </Modal>

    <Modal title="告警处理记录" open={eventsOpen} footer={null} onCancel={() => setEventsOpen(false)} width={760}>
      <Table rowKey="id" dataSource={events} pagination={false} columns={[
        { title: '时间', dataIndex: 'created_at' },
        { title: '动作', dataIndex: 'action', render: v => <Tag color={v === 'resolve' ? 'green' : 'blue'}>{v}</Tag> },
        { title: '处理人', dataIndex: 'actor_email' },
        { title: '说明', dataIndex: 'note' }
      ]} />
    </Modal>
  </>;
}
