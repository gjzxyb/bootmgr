import { DeleteOutlined, EditOutlined, ReloadOutlined, UploadOutlined } from '@ant-design/icons';
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography, Upload, message } from 'antd';
import { useEffect, useState } from 'react';
import { api, Image, PageResult } from '../api/client';
import { canManage, isAdmin } from '../authz';

const statusColor = (status: string) => status === 'enabled' ? 'blue' : 'default';
const testColor = (status: string) => status === 'tested_passed' ? 'green' : status === 'test_failed' ? 'red' : 'orange';
const formatBytes = (value?: number) => {
  if (!value) return '-';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GB`;
};

export function ImagesPage({ role }: { role?: string }) {
  const canWrite = canManage(role);
  const admin = isAdmin(role);
  const [rows, setRows] = useState<Image[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Image | null>(null);
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [form] = Form.useForm();
  const [filterForm] = Form.useForm();
  const [msg, holder] = message.useMessage();

  const load = async (nextPage = page, nextPageSize = pageSize) => {
    const values = filterForm.getFieldsValue();
    const { data } = await api.get<PageResult<Image>>('/images', { params: { ...values, page: nextPage, page_size: nextPageSize } });
    setRows(data.items);
    setTotal(data.total);
    setPage(data.page);
    setPageSize(data.page_size);
  };

  useEffect(() => { void load(1, pageSize); }, []);

  const openCreate = () => {
    setEditing(null);
    setUploadFile(null);
    form.setFieldsValue({ architecture: 'x86_64', status: 'enabled', test_status: 'untested' });
    setOpen(true);
  };

  const openEdit = (image: Image) => {
    setEditing(image);
    setUploadFile(null);
    form.setFieldsValue(image);
    setOpen(true);
  };

  const save = async () => {
    const values = await form.validateFields();
    if (editing) {
      await api.patch(`/images/${editing.id}`, values);
      msg.success('镜像已更新');
    } else if (uploadFile) {
      const body = new FormData();
      body.append('file', uploadFile);
      Object.entries(values).forEach(([key, value]) => {
        if (value !== undefined && value !== null && value !== '') body.append(key, String(value));
      });
      await api.post('/images/upload', body);
      msg.success('镜像已上传并校验');
    } else {
      if (!values.file_path) {
        form.setFields([{ name: 'file_path', errors: ['请选择文件或填写后端文件路径'] }]);
        return;
      }
      await api.post('/images', values);
      msg.success('镜像已创建');
    }
    setOpen(false);
    setEditing(null);
    setUploadFile(null);
    form.resetFields();
    void load(editing ? page : 1, pageSize);
  };

  const verify = async (id: number) => {
    try {
      await api.post(`/images/${id}/verify`, {}, { suppressGlobalError: true });
      msg.success('镜像文件校验通过');
    } catch {
      msg.error('镜像文件不存在或校验失败');
    }
    void load(page, pageSize);
  };

  const toggle = async (image: Image) => {
    const status = image.status === 'enabled' ? 'disabled' : 'enabled';
    await api.patch(`/images/${image.id}`, { status });
    msg.success(status === 'enabled' ? '镜像已启用' : '镜像已禁用');
    void load(page, pageSize);
  };

  const remove = async (image: Image) => {
    await api.delete(`/images/${image.id}`, { headers: { 'X-Confirm-Action': 'image.delete' } });
    msg.success('镜像已删除');
    void load(1, pageSize);
  };

  return <>
    {holder}
    <Typography.Title level={3} className="page-title">镜像管理</Typography.Title>
    <div className="toolbar">
      <Space>{canWrite && <Button type="primary" onClick={openCreate}>登记镜像</Button>}<Button icon={<ReloadOutlined />} onClick={() => load(page, pageSize)}>刷新</Button></Space>
      <Form form={filterForm} layout="inline" onFinish={() => load(1, pageSize)} style={{ marginTop: 12 }}>
        <Form.Item name="keyword"><Input placeholder="名称/版本/SHA" allowClear /></Form.Item>
        <Form.Item name="os_family"><Input placeholder="OS Family" allowClear /></Form.Item>
        <Form.Item name="architecture"><Select placeholder="架构" allowClear style={{ width: 130 }} options={['x86_64', 'arm64'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="status"><Select placeholder="状态" allowClear style={{ width: 130 }} options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="test_status"><Select placeholder="测试状态" allowClear style={{ width: 160 }} options={['untested', 'tested_passed', 'test_failed'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Space><Button type="primary" htmlType="submit">查询</Button><Button onClick={() => { filterForm.resetFields(); void load(1, pageSize); }}>重置</Button></Space>
      </Form>
    </div>
    <Table rowKey="id" dataSource={rows} pagination={{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => load(p, ps) }} columns={[
      { title: '名称', dataIndex: 'name' },
      { title: '系统', render: (_, r) => `${r.os_family} ${r.os_version}` },
      { title: '架构', dataIndex: 'architecture' },
      { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
      { title: '测试状态', dataIndex: 'test_status', render: v => <Tag color={testColor(v)}>{v}</Tag> },
      { title: '大小', dataIndex: 'size_bytes', render: formatBytes },
      { title: 'SHA256', dataIndex: 'sha256' },
      { title: '操作', render: (_, r) => canWrite || admin ? <Space>
        {canWrite && <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)}>编辑</Button>}
        {canWrite && <Button size="small" onClick={() => toggle(r)}>{r.status === 'enabled' ? '禁用' : '启用'}</Button>}
        {canWrite && <Button size="small" onClick={() => verify(r.id)}>校验文件</Button>}
        {admin && <Popconfirm title="确认删除该镜像？" onConfirm={() => remove(r)}><Button size="small" danger icon={<DeleteOutlined />}>删除</Button></Popconfirm>}
      </Space> : '-' }
    ]} />
    <Modal title={editing ? '编辑镜像' : '登记镜像'} open={open} onOk={save} onCancel={() => { setOpen(false); setEditing(null); setUploadFile(null); }} forceRender>
      <Form form={form} layout="vertical" initialValues={{ architecture: 'x86_64', status: 'enabled', test_status: 'untested' }}>
        {!editing && <Form.Item label="镜像文件">
          <Upload
            beforeUpload={file => { setUploadFile(file); return false; }}
            maxCount={1}
            fileList={uploadFile ? [{ uid: 'selected-image', name: uploadFile.name, status: 'done' }] : []}
            onRemove={() => { setUploadFile(null); return true; }}
          >
            <Button icon={<UploadOutlined />}>选择文件</Button>
          </Upload>
        </Form.Item>}
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="os_family" label="OS Family"><Input /></Form.Item>
        <Form.Item name="os_version" label="版本"><Input /></Form.Item>
        <Form.Item name="architecture" label="架构"><Select options={['x86_64', 'arm64'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="status" label="状态"><Select options={['enabled', 'disabled'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="test_status" label="测试状态"><Select disabled options={['untested', 'tested_passed', 'test_failed'].map(v => ({ value: v, label: v }))} /></Form.Item>
        <Form.Item name="file_path" label="文件路径"><Input /></Form.Item>
        <Form.Item name="sha256" label="SHA256"><Input disabled /></Form.Item>
      </Form>
    </Modal>
  </>;
}
