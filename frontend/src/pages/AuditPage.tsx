import { EyeOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { Button, Form, Input, Modal, Select, Space, Table, Tag, Typography } from 'antd';
import { useEffect, useState } from 'react';
import { api, AuditLog, PageResult } from '../api/client';

const riskColor = (risk: string) => risk === 'high' ? 'red' : risk === 'medium' ? 'orange' : 'green';

export function AuditPage() {
  const [rows, setRows] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [detail, setDetail] = useState<AuditLog | null>(null);
  const [form] = Form.useForm();

  const load = async (nextPage = page, nextPageSize = pageSize) => {
    const values = form.getFieldsValue();
    const { data } = await api.get<PageResult<AuditLog>>('/audit-logs', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRows(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };

  useEffect(() => { void load(1, pageSize); }, []);

  const showDetail = async (row: AuditLog) => {
    const { data } = await api.get(`/audit-logs/${row.id}`);
    setDetail(data);
  };

  return <>
    <Typography.Title level={3} className="page-title">审计日志</Typography.Title>
    <div className="toolbar">
      <Form form={form} layout="inline" onFinish={() => load(1, pageSize)}>
        <Form.Item name="action"><Input placeholder="动作" allowClear /></Form.Item>
        <Form.Item name="actor_email"><Input placeholder="用户邮箱" allowClear /></Form.Item>
        <Form.Item name="resource_type"><Input placeholder="资源类型" allowClear /></Form.Item>
        <Form.Item name="risk_level"><Select placeholder="风险" allowClear style={{ width: 120 }} options={[{ value: 'low', label: 'low' }, { value: 'medium', label: 'medium' }, { value: 'high', label: 'high' }]} /></Form.Item>
        <Space>
          <Button type="primary" htmlType="submit" icon={<SearchOutlined />}>查询</Button>
          <Button icon={<ReloadOutlined />} onClick={() => { form.resetFields(); void load(1, pageSize); }}>重置</Button>
        </Space>
      </Form>
    </div>
    <Table rowKey="id" dataSource={rows} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => load(p, ps) }} columns={[
      { title: '时间', dataIndex: 'created_at' },
      { title: '用户', dataIndex: 'actor_email' },
      { title: '动作', dataIndex: 'action' },
      { title: '资源', render: (_, r) => `${r.resource_type}:${r.resource_id}` },
      { title: '风险', dataIndex: 'risk_level', render: v => <Tag color={riskColor(v)}>{v}</Tag> },
      { title: '来源 IP', dataIndex: 'client_ip' },
      { title: '操作', render: (_, r) => <Button size="small" icon={<EyeOutlined />} onClick={() => showDetail(r)}>详情</Button> }
    ]} />
    <Modal title="审计详情" open={!!detail} footer={null} onCancel={() => setDetail(null)} width={760}>
      <pre style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{JSON.stringify(detail, null, 2)}</pre>
    </Modal>
  </>;
}
