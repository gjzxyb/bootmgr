import { CodeOutlined, DownloadOutlined, FileSearchOutlined, LaptopOutlined, ReloadOutlined, UploadOutlined } from '@ant-design/icons';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { useEffect, useRef, useState } from 'react';
import { api, apiErrorMessage, BackupRestoreResult, BackupValidationCheck, BackupValidationReport, PageResult, ScriptExecution, ScriptJob, Server, TerminalSession } from '../api/client';
import { canManage, isAdmin } from '../authz';

const color = (status: string) => status === 'success' || status === 'closed' ? 'green' : status === 'running' || status === 'active' ? 'blue' : status === 'failed' ? 'red' : 'default';
const checkColor = (status: string) => status === 'ok' ? 'green' : status === 'warning' ? 'orange' : 'red';
type LogCollectionResult = { server_id: number; status: string; events?: number[]; error?: string };
type LogCollectionResponse = { requested: number; succeeded: number; failed: number; events_created: number; results: LogCollectionResult[] };

export function OpsPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const admin = isAdmin(role);
  const [jobs, setJobs] = useState<ScriptJob[]>([]);
  const [sessions, setSessions] = useState<TerminalSession[]>([]);
  const [jobTotal, setJobTotal] = useState(0);
  const [sessionTotal, setSessionTotal] = useState(0);
  const [jobPage, setJobPage] = useState(1);
  const [sessionPage, setSessionPage] = useState(1);
  const [jobPageSize, setJobPageSize] = useState(20);
  const [sessionPageSize, setSessionPageSize] = useState(20);
  const [servers, setServers] = useState<Server[]>([]);
  const [scriptOpen, setScriptOpen] = useState(false);
  const [logOpen, setLogOpen] = useState(false);
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [resultOpen, setResultOpen] = useState(false);
  const [transcriptOpen, setTranscriptOpen] = useState(false);
  const [results, setResults] = useState<ScriptExecution[]>([]);
  const [transcript, setTranscript] = useState('');
  const [activeTranscriptSession, setActiveTranscriptSession] = useState<TerminalSession | null>(null);
  const [terminalCommand, setTerminalCommand] = useState('');
  const [logResults, setLogResults] = useState<LogCollectionResult[]>([]);
  const [logSummary, setLogSummary] = useState<LogCollectionResponse | null>(null);
  const [backupReport, setBackupReport] = useState<BackupValidationReport | null>(null);
  const [backupPayloadText, setBackupPayloadText] = useState('');
  const [scriptCreating, setScriptCreating] = useState(false);
  const [logCollecting, setLogCollecting] = useState(false);
  const [terminalCreating, setTerminalCreating] = useState(false);
  const [terminalCommandRunning, setTerminalCommandRunning] = useState(false);
  const [backupValidating, setBackupValidating] = useState(false);
  const [backupRestoring, setBackupRestoring] = useState(false);
  const [scriptForm] = Form.useForm();
  const [logForm] = Form.useForm();
  const [terminalForm] = Form.useForm();
  const [scriptFilterForm] = Form.useForm();
  const [terminalFilterForm] = Form.useForm();
  const backupInputRef = useRef<HTMLInputElement | null>(null);
  const [msg, holder] = message.useMessage();

  const loadJobs = async (nextPage = jobPage, nextPageSize = jobPageSize) => {
    const values = scriptFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<ScriptJob>>('/ops/script-jobs', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setJobs(data.items);
    setJobTotal(data.total);
    setJobPage(data.page);
    setJobPageSize(data.page_size);
  };
  const loadSessions = async (nextPage = sessionPage, nextPageSize = sessionPageSize) => {
    const values = terminalFilterForm.getFieldsValue();
    const { data } = await api.get<PageResult<TerminalSession>>('/ops/terminal-sessions', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setSessions(data.items);
    setSessionTotal(data.total);
    setSessionPage(data.page);
    setSessionPageSize(data.page_size);
  };
  const loadServers = async () => {
    const { data } = await api.get('/servers');
    setServers(data);
  };

  useEffect(() => {
    void loadJobs(1, jobPageSize);
    void loadSessions(1, sessionPageSize);
  }, []);

  const openCreateScript = async () => {
    await loadServers();
    scriptForm.resetFields();
    scriptForm.setFieldsValue({ name: '巡检 uptime', script: 'uptime', concurrency: 5, timeout_seconds: 60 });
    setScriptOpen(true);
  };

  const createScript = async () => {
    if (scriptCreating) return;
    const values = await scriptForm.validateFields();
    setScriptCreating(true);
    try {
      await api.post('/ops/script-jobs', values, { headers: { 'X-Confirm-Action': 'ops.script.create' }, suppressGlobalError: true });
      msg.success('批量脚本任务已创建');
      setScriptOpen(false);
      scriptForm.resetFields();
      setTimeout(() => loadJobs(1, jobPageSize), 700);
    } catch (error) {
      msg.error(apiErrorMessage(error, '批量脚本任务创建失败'));
    } finally {
      setScriptCreating(false);
    }
  };

  const showResults = async (job: ScriptJob) => {
    const { data } = await api.get(`/ops/script-jobs/${job.id}/results`);
    setResults(data);
    setResultOpen(true);
  };

  const openLogCollection = async () => {
    await loadServers();
    logForm.resetFields();
    logForm.setFieldsValue({ sources: ['syslog', 'dmesg', 'hardware'] });
    setLogOpen(true);
  };

  const collectLogs = async () => {
    if (logCollecting) return;
    const values = await logForm.validateFields();
    setLogCollecting(true);
    try {
      const { data } = await api.post<LogCollectionResponse>('/ops/log-collections', values, { headers: { 'X-Confirm-Action': 'ops.logs.collect' }, suppressGlobalError: true });
      setLogSummary(data);
      setLogResults(data.results || []);
      if (data.failed > 0) msg.warning(`日志采集完成：成功 ${data.succeeded}，失败 ${data.failed}`);
      else msg.success(`日志采集完成：生成 ${data.events_created} 条事件`);
      setLogOpen(false);
      logForm.resetFields();
    } catch (error) {
      msg.error(apiErrorMessage(error, '日志采集失败'));
    } finally {
      setLogCollecting(false);
    }
  };

  const openTerminal = async () => {
    await loadServers();
    terminalForm.resetFields();
    terminalForm.setFieldsValue({ reason: 'break-glass inspection' });
    setTerminalOpen(true);
  };

  const createTerminal = async () => {
    if (terminalCreating) return;
    const values = await terminalForm.validateFields();
    setTerminalCreating(true);
    try {
      await api.post('/ops/terminal-sessions', values, { headers: { 'X-Confirm-Action': 'ops.terminal.open' }, suppressGlobalError: true });
      msg.success('终端会话已打开');
      setTerminalOpen(false);
      terminalForm.resetFields();
      void loadSessions(1, sessionPageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '终端会话打开失败'));
    } finally {
      setTerminalCreating(false);
    }
  };

  const showTranscript = async (session: TerminalSession) => {
    const { data } = await api.get(`/ops/terminal-sessions/${session.id}`);
    setActiveTranscriptSession(data);
    setTranscript(data.transcript || '');
    setTerminalCommand('');
    setTranscriptOpen(true);
  };

  const runTerminalCommand = async () => {
    if (!activeTranscriptSession || !terminalCommand.trim() || terminalCommandRunning) return;
    setTerminalCommandRunning(true);
    try {
      const { data } = await api.post<TerminalSession>(`/ops/terminal-sessions/${activeTranscriptSession.id}/commands`, { command: terminalCommand }, { headers: { 'X-Confirm-Action': 'ops.terminal.command' }, suppressGlobalError: true });
      setActiveTranscriptSession(data);
      setTranscript(data.transcript || '');
      setTerminalCommand('');
      msg.success('命令已执行');
      void loadSessions(sessionPage, sessionPageSize);
    } catch (error) {
      msg.error(apiErrorMessage(error, '命令执行失败'));
    } finally {
      setTerminalCommandRunning(false);
    }
  };

  const closeTerminal = (session: TerminalSession) => {
    Modal.confirm({
      title: '关闭终端会话',
      content: `会话 #${session.id} 将被标记为 closed。`,
      okText: '关闭',
      cancelText: '取消',
      onOk: async () => {
        await api.post(`/ops/terminal-sessions/${session.id}/close`, {}, { headers: { 'X-Confirm-Action': 'ops.terminal.close' } });
        msg.success('终端会话已关闭');
        void loadSessions(sessionPage, sessionPageSize);
      }
    });
  };

  const downloadBlob = (data: BlobPart, filename: string, type = 'application/json;charset=utf-8') => {
    const url = URL.createObjectURL(new Blob([data], { type }));
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  };

  const exportBackup = async () => {
    const res = await api.get('/ops/backup/export', { responseType: 'blob', headers: { 'X-Confirm-Action': 'ops.backup.export' } });
    downloadBlob(res.data, `baremetal-backup-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.json`);
    msg.success('备份文件已生成');
  };

  const validateBackupFile = async (file: File) => {
    setBackupValidating(true);
    setBackupPayloadText('');
    setBackupReport(null);
    try {
      const text = await file.text();
      const { data } = await api.post<BackupValidationReport>('/ops/backup/validate', text, { headers: { 'Content-Type': 'application/json' }, suppressGlobalError: true });
      setBackupPayloadText(text);
      setBackupReport(data);
      if (data.status === 'error') msg.error('备份预检发现错误');
      else if (data.status === 'warning') msg.warning('备份预检存在警告');
      else msg.success('备份预检通过');
    } catch (error) {
      msg.error(apiErrorMessage(error, '备份预检失败'));
    } finally {
      setBackupValidating(false);
    }
  };

  const restoreBackup = () => {
    if (!backupPayloadText || !backupReport || backupReport.status === 'error') {
      msg.error('请先选择并通过备份预检');
      return;
    }
    Modal.confirm({
      title: '执行备份恢复',
      content: '恢复会写入备份数据，仅适用于 fresh 目标库；现有业务数据会被拒绝恢复。',
      okText: '执行恢复',
      cancelText: '取消',
      okButtonProps: { danger: true },
      onOk: async () => {
        setBackupRestoring(true);
        try {
          const { data } = await api.post<BackupRestoreResult>('/ops/backup/restore', backupPayloadText, { headers: { 'Content-Type': 'application/json', 'X-Confirm-Action': 'ops.backup.restore' } });
          const total = Object.values(data.imported).reduce((sum, count) => sum + count, 0);
          msg.success(`备份恢复完成，导入 ${total} 条记录`);
        } finally {
          setBackupRestoring(false);
        }
      }
    });
  };

  const backupTotals = Object.entries(backupReport?.totals || {}).map(([name, count]) => ({ name, count }));
  const targetCounts = Object.entries(backupReport?.target_counts || {}).map(([name, count]) => ({ name, count }));
  const serverTargetOptions = servers
    .filter(server => server.status !== 'retired' && server.status !== 'scrapped')
    .map(server => ({ value: server.id, label: `${server.hostname || server.asset_no} (${server.status})` }));

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">运维工具</Typography.Title>
    <Tabs items={[
      {
        key: 'scripts',
        label: '批量脚本',
        children: <>
          <div className="toolbar">
            <Space>{canWrite && <Button type="primary" icon={<CodeOutlined />} onClick={openCreateScript}>批量脚本</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadJobs(jobPage, jobPageSize)}>刷新</Button></Space>
            <Form form={scriptFilterForm} layout="inline" onFinish={() => loadJobs(1, jobPageSize)} style={{ marginTop: 12 }}>
              <Form.Item name="keyword"><Input placeholder="名称/脚本" allowClear /></Form.Item>
              <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={['running', 'success', 'failed'].map(v => ({ value: v, label: v }))} /></Form.Item>
              <Form.Item name="requested_by"><Input placeholder="发起人" allowClear /></Form.Item>
              <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { scriptFilterForm.resetFields(); void loadJobs(1, jobPageSize); }}>重置</Button></Space>
            </Form>
          </div>
          <Table rowKey="id" dataSource={jobs} pagination={{ current: jobPage, pageSize: jobPageSize, total: jobTotal, showSizeChanger: true, onChange: (p, ps) => loadJobs(p, ps) }} columns={[
            { title: 'ID', dataIndex: 'id' },
            { title: '名称', dataIndex: 'name' },
            { title: '状态', dataIndex: 'status', render: v => <Tag color={color(v)}>{v}</Tag> },
            { title: '发起人', dataIndex: 'requested_by' },
            { title: '并发', dataIndex: 'concurrency' },
            { title: '超时', dataIndex: 'timeout_seconds' },
            { title: '创建时间', dataIndex: 'created_at' },
            { title: '操作', render: (_, r) => <Button size="small" onClick={() => showResults(r)}>结果</Button> }
          ]} />
        </>
      },
      {
        key: 'logs',
        label: '日志采集',
        children: <>
          <div className="toolbar">
            <Space>{canWrite && <Button type="primary" icon={<FileSearchOutlined />} onClick={openLogCollection}>一键采集日志</Button>}</Space>
          </div>
          {logSummary && <Typography.Paragraph type={logSummary.failed > 0 ? 'warning' : 'success'}>
            请求 {logSummary.requested} 台，成功 {logSummary.succeeded}，失败 {logSummary.failed}，事件 {logSummary.events_created} 条
          </Typography.Paragraph>}
          <Table rowKey="server_id" dataSource={logResults} pagination={false} columns={[
            { title: '服务器', dataIndex: 'server_id' },
            { title: '状态', dataIndex: 'status', render: v => <Tag color={color(v)}>{v}</Tag> },
            { title: '事件', render: (_, r) => r.events?.join(', ') || '-' },
            { title: '错误', dataIndex: 'error', render: v => v || '-' }
          ]} />
        </>
      },
      {
        key: 'terminal',
        label: '远程终端',
        forceRender: true,
        children: <>
          <div className="toolbar">
            <Space>{canWrite && <Button type="primary" icon={<LaptopOutlined />} onClick={openTerminal}>打开终端</Button>}<Button icon={<ReloadOutlined />} onClick={() => loadSessions(sessionPage, sessionPageSize)}>刷新</Button></Space>
            <Form form={terminalFilterForm} layout="inline" onFinish={() => loadSessions(1, sessionPageSize)} style={{ marginTop: 12 }}>
              <Form.Item name="server_id"><Input placeholder="服务器 ID" allowClear /></Form.Item>
              <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={['active', 'closed'].map(v => ({ value: v, label: v }))} /></Form.Item>
              <Form.Item name="mode"><Select placeholder="模式" allowClear style={{ width: 130 }} options={['simulated', 'ssh'].map(v => ({ value: v, label: v }))} /></Form.Item>
              <Form.Item name="requested_by"><Input placeholder="发起人" allowClear /></Form.Item>
              <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { terminalFilterForm.resetFields(); void loadSessions(1, sessionPageSize); }}>重置</Button></Space>
            </Form>
          </div>
          <Table rowKey="id" dataSource={sessions} pagination={{ current: sessionPage, pageSize: sessionPageSize, total: sessionTotal, showSizeChanger: true, onChange: (p, ps) => loadSessions(p, ps) }} columns={[
            { title: 'ID', dataIndex: 'id' },
            { title: '服务器', dataIndex: 'server_id' },
            { title: '状态', dataIndex: 'status', render: v => <Tag color={color(v)}>{v}</Tag> },
            { title: '模式', dataIndex: 'mode' },
            { title: '发起人', dataIndex: 'requested_by' },
            { title: '原因', dataIndex: 'reason' },
            { title: '打开时间', dataIndex: 'opened_at' },
            { title: '关闭时间', dataIndex: 'closed_at', render: v => v || '-' },
            { title: '操作', render: (_, r) => <Space><Button size="small" onClick={() => showTranscript(r)}>记录</Button>{canWrite && <Button size="small" danger disabled={r.status === 'closed'} onClick={() => closeTerminal(r)}>关闭</Button>}</Space> }
          ]} />
        </>
      },
      ...(admin ? [{
        key: 'backup',
        label: '备份与恢复',
        children: <>
          <div className="toolbar">
            <Space>
              <Button type="primary" icon={<DownloadOutlined />} onClick={exportBackup}>导出备份 JSON</Button>
              <Button icon={<UploadOutlined />} loading={backupValidating} onClick={() => backupInputRef.current?.click()}>校验备份 JSON</Button>
              <Button danger loading={backupRestoring} disabled={!backupPayloadText || backupReport?.status === 'error'} onClick={restoreBackup}>执行恢复</Button>
              {backupReport && <Tag color={checkColor(backupReport.status)}>预检 {backupReport.status}</Tag>}
            </Space>
            <input ref={backupInputRef} type="file" accept="application/json,.json" style={{ display: 'none' }} onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = '';
              if (file) void validateBackupFile(file);
            }} />
          </div>
          <Typography.Paragraph type="secondary">
            备份导出包含资产、镜像、模板、部署、监控、运维任务、告警和审计数据；不会导出凭据密文。恢复执行需要 fresh 目标库和二次确认。
          </Typography.Paragraph>
          {backupReport && <>
            <Table<BackupValidationCheck> rowKey="name" dataSource={backupReport.checks} pagination={false} columns={[
              { title: '检查项', dataIndex: 'name' },
              { title: '状态', dataIndex: 'status', render: value => <Tag color={checkColor(value)}>{value}</Tag> },
              { title: '结果', dataIndex: 'message' }
            ]} />
            <Typography.Title level={5} style={{ marginTop: 18 }}>备份数据量</Typography.Title>
            <Table rowKey="name" dataSource={backupTotals} pagination={false} size="small" columns={[
              { title: '集合', dataIndex: 'name' },
              { title: '记录数', dataIndex: 'count' }
            ]} />
            <Typography.Title level={5} style={{ marginTop: 18 }}>目标库现有数据</Typography.Title>
            <Table rowKey="name" dataSource={targetCounts} pagination={false} size="small" columns={[
              { title: '集合', dataIndex: 'name' },
              { title: '记录数', dataIndex: 'count' }
            ]} />
          </>}
        </>
      }] : [])
    ]} />

    <Modal title="创建批量脚本任务" open={scriptOpen} onOk={createScript} confirmLoading={scriptCreating} onCancel={() => setScriptOpen(false)} width={760} forceRender>
      <Form form={scriptForm} layout="vertical">
        <Form.Item name="name" label="任务名称" rules={[{ required: true }]}><Input /></Form.Item>
        {serverTargetOptions.length === 0 && <Alert type="warning" showIcon message="暂无可用目标服务器" style={{ marginBottom: 12 }} />}
        <Form.Item name="server_ids" label="目标服务器" rules={[{ required: true }]}><Select mode="multiple" options={serverTargetOptions} /></Form.Item>
        <Space.Compact block><Form.Item name="concurrency" label="并发" style={{ width: '50%' }}><InputNumber min={1} max={50} style={{ width: '100%' }} /></Form.Item><Form.Item name="timeout_seconds" label="超时秒数" style={{ width: '50%' }}><InputNumber min={1} max={3600} style={{ width: '100%' }} /></Form.Item></Space.Compact>
        <Form.Item name="script" label="脚本内容" rules={[{ required: true }]}><Input.TextArea rows={8} /></Form.Item>
      </Form>
    </Modal>

    <Modal title="一键采集日志" open={logOpen} onOk={collectLogs} confirmLoading={logCollecting} onCancel={() => setLogOpen(false)} width={680} forceRender>
      <Form form={logForm} layout="vertical">
        {serverTargetOptions.length === 0 && <Alert type="warning" showIcon message="暂无可用目标服务器" style={{ marginBottom: 12 }} />}
        <Form.Item name="server_ids" label="目标服务器" rules={[{ required: true }]}><Select mode="multiple" options={serverTargetOptions} /></Form.Item>
        <Form.Item name="sources" label="日志来源" rules={[{ required: true }]}><Select mode="multiple" options={[
          { value: 'syslog', label: 'syslog' },
          { value: 'dmesg', label: 'dmesg' },
          { value: 'hardware', label: 'hardware' }
        ]} /></Form.Item>
      </Form>
    </Modal>

    <Modal title="打开远程终端" open={terminalOpen} onOk={createTerminal} confirmLoading={terminalCreating} onCancel={() => setTerminalOpen(false)} width={620} forceRender>
      <Form form={terminalForm} layout="vertical">
        {serverTargetOptions.length === 0 && <Alert type="warning" showIcon message="暂无可用目标服务器" style={{ marginBottom: 12 }} />}
        <Form.Item name="server_id" label="服务器" rules={[{ required: true }]}><Select options={serverTargetOptions} /></Form.Item>
        <Form.Item name="reason" label="操作原因" rules={[{ required: true }]}><Input /></Form.Item>
      </Form>
    </Modal>

    <Modal title="脚本执行结果" open={resultOpen} footer={null} onCancel={() => setResultOpen(false)} width={860}>
      <Table rowKey="id" dataSource={results} pagination={false} columns={[
        { title: '服务器', dataIndex: 'server_id' },
        { title: '状态', dataIndex: 'status', render: v => <Tag color={color(v)}>{v}</Tag> },
        { title: '退出码', dataIndex: 'exit_code' },
        { title: '输出', dataIndex: 'stdout' }
      ]} />
    </Modal>

    <Modal title="终端会话记录" open={transcriptOpen} footer={null} onCancel={() => setTranscriptOpen(false)} width={760}>
      <Space.Compact style={{ width: '100%', marginBottom: 12 }}>
        <Input value={terminalCommand} onChange={event => setTerminalCommand(event.target.value)} onPressEnter={runTerminalCommand} placeholder="输入命令" disabled={!canWrite || activeTranscriptSession?.status === 'closed' || terminalCommandRunning} />
        <Button type="primary" onClick={runTerminalCommand} loading={terminalCommandRunning} disabled={!canWrite || activeTranscriptSession?.status === 'closed' || !terminalCommand.trim()}>执行</Button>
      </Space.Compact>
      <Input.TextArea value={transcript} rows={10} readOnly />
    </Modal>
  </>;
}
