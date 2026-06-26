import { DeleteOutlined, EditOutlined, ReloadOutlined, UploadOutlined } from '@ant-design/icons';
import { Button, Form, Input, Modal, Popconfirm, Segmented, Select, Space, Table, Tag, Typography, Upload, message } from 'antd';
import axios from 'axios';
import { useEffect, useState } from 'react';
import { api, apiErrorMessage, Image, PageResult } from '../api/client';
import { canManage, isAdmin } from '../authz';

type CreateMode = 'upload' | 'path';

const statusColor = (status: string) => status === 'enabled' ? 'blue' : 'default';
const testColor = (status: string) => status === 'tested_passed' ? 'green' : status === 'test_failed' ? 'red' : 'orange';
const defaultBootTags = {
  kernel_url: 'ubuntu-24.04/casper/vmlinuz',
  initrd_url: 'ubuntu-24.04/casper/initrd',
  kernel_params: 'ip=dhcp boot=casper netboot=url url={{image_url}} autoinstall ds=nocloud-net;s={{metadata_url}}/'
};
const formatBytes = (value?: number) => {
  if (!value) return '-';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GB`;
};
const configuredImageUploadMaxMB = () => {
  const value = Number(import.meta.env.VITE_IMAGE_UPLOAD_MAX_MB);
  return Number.isFinite(value) && value > 0 ? value : 20;
};
const imageUploadMaxBytes = () => configuredImageUploadMaxMB() * 1024 * 1024;
const imageUploadTooLargeMessage = (maxBytes: number) =>
  `镜像文件超过上传大小限制（最大 ${formatBytes(maxBytes)}）；请调整 IMAGE_UPLOAD_MAX_MB 后重试`;
const imageUploadErrorMessage = (error: unknown) => {
  if (!axios.isAxiosError(error)) return '镜像上传失败，请稍后重试';
  const responseError = error.response?.data?.error;
  if (error.response?.status === 413) {
    return typeof responseError === 'string'
      ? responseError
      : imageUploadTooLargeMessage(imageUploadMaxBytes());
  }
  if (!error.response) {
    return '镜像上传连接被中断，可能是文件超过后端上传大小限制；请调整 IMAGE_UPLOAD_MAX_MB 或选择更小文件';
  }
  return typeof responseError === 'string' ? responseError : '镜像上传失败，请稍后重试';
};
const imageMutationErrorMessage = (error: unknown, fallback: string) => {
  const detail = apiErrorMessage(error, fallback);
  const lower = detail.toLowerCase();
  if (lower.includes('file_path is required')) return '请填写后端文件路径';
  if (lower.includes('file_path must be inside image_storage_dir') || lower.includes('file_path must resolve inside image_storage_dir')) {
    return '文件路径必须位于后端 IMAGE_STORAGE_DIR 内';
  }
  if (lower.includes('resolve file_path')) return '无法解析文件路径，请确认文件位于 IMAGE_STORAGE_DIR 内';
  if (lower.includes('name is required')) return '请填写名称';
  return detail;
};
const isFormValidationError = (error: unknown) =>
  Boolean(error && typeof error === 'object' && 'errorFields' in error);
const uploadDefaultName = (filename: string) => filename.replace(/\.[^/.]+$/, '') || filename;

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
  const [uploadError, setUploadError] = useState('');
  const [createMode, setCreateMode] = useState<CreateMode>('upload');
  const [saving, setSaving] = useState(false);
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
    setUploadError('');
    setCreateMode('upload');
    form.resetFields();
    form.setFieldsValue({ architecture: 'x86_64', status: 'enabled', test_status: 'untested', tags_text: JSON.stringify(defaultBootTags, null, 2) });
    setOpen(true);
  };

  const openEdit = (image: Image) => {
    setEditing(image);
    setUploadFile(null);
    setUploadError('');
    form.resetFields();
    form.setFieldsValue({ ...image, tags_text: image.tags ? JSON.stringify(image.tags, null, 2) : '' });
    setOpen(true);
  };

  const closeModal = () => {
    setOpen(false);
    setEditing(null);
    setUploadFile(null);
    setUploadError('');
    form.resetFields();
  };

  const switchCreateMode = (mode: CreateMode) => {
    setCreateMode(mode);
    setUploadFile(null);
    setUploadError('');
    form.setFields([{ name: 'file_path', errors: [] }]);
  };

  const showImageMutationError = (error: unknown, fallback: string) => {
    const detail = imageMutationErrorMessage(error, fallback);
    if (detail.includes('文件路径') || detail.includes('IMAGE_STORAGE_DIR')) {
      form.setFields([{ name: 'file_path', errors: [detail] }]);
      return;
    }
    if (detail === '请填写名称') {
      form.setFields([{ name: 'name', errors: [detail] }]);
      return;
    }
    msg.error(detail);
  };

  const save = async () => {
    if (saving) return;
    try {
      const values = await form.validateFields();
      const rawTags = String(values.tags_text || '').trim();
      delete values.tags_text;
      if (rawTags) {
        try {
          values.tags = JSON.parse(rawTags);
        } catch {
          form.setFields([{ name: 'tags_text', errors: ['请输入合法 JSON'] }]);
          return;
        }
      } else {
        values.tags = null;
      }
      if (!editing && createMode === 'upload') {
        delete values.file_path;
        const selectedFile = uploadFile;
        if (!selectedFile) {
          setUploadError('请选择镜像文件');
          return;
        }
        values.upload_file = selectedFile;
      } else {
        values.file_path = String(values.file_path || '').trim();
        if (!values.file_path) {
          form.setFields([{ name: 'file_path', errors: ['请填写后端文件路径'] }]);
          return;
        }
      }
      setSaving(true);
      if (editing) {
        await api.patch(`/images/${editing.id}`, values, { suppressGlobalError: true });
        msg.success('镜像已更新');
      } else if (createMode === 'upload') {
        const selectedFile = values.upload_file as File;
        delete values.upload_file;
        const maxBytes = imageUploadMaxBytes();
        if (selectedFile.size > maxBytes) {
          msg.error(imageUploadTooLargeMessage(maxBytes));
          return;
        }
        const body = new FormData();
        body.append('file', selectedFile);
        Object.entries(values).forEach(([key, value]) => {
          if (value !== undefined && value !== null && value !== '') body.append(key, key === 'tags' ? JSON.stringify(value) : String(value));
        });
        await api.post('/images/upload', body, { suppressGlobalError: true });
        msg.success('镜像已上传并校验');
      } else {
        await api.post('/images', values, { suppressGlobalError: true });
        msg.success('镜像已创建');
      }
      closeModal();
      void load(editing ? page : 1, pageSize);
    } catch (error) {
      if (isFormValidationError(error)) return;
      if (!editing && createMode === 'upload') {
        msg.error(imageUploadErrorMessage(error));
        return;
      }
      showImageMutationError(error, editing ? '镜像更新失败' : '镜像登记失败');
    } finally {
      setSaving(false);
    }
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
    <Modal
      title={editing ? '编辑镜像' : '登记镜像'}
      open={open}
      okText={editing ? '保存' : createMode === 'upload' ? '上传并登记' : '登记'}
      onOk={() => { void save(); }}
      onCancel={closeModal}
      confirmLoading={saving}
      forceRender
    >
      <Form form={form} layout="vertical" initialValues={{ architecture: 'x86_64', status: 'enabled', test_status: 'untested' }}>
        {!editing && <Form.Item label="登记方式">
          <Segmented
            value={createMode}
            options={[
              { label: '上传文件', value: 'upload' },
              { label: '后端路径', value: 'path' }
            ]}
            onChange={value => switchCreateMode(value as CreateMode)}
          />
        </Form.Item>}
        {!editing && createMode === 'upload' && <Form.Item label="镜像文件" validateStatus={uploadError ? 'error' : undefined} help={uploadError}>
          <Upload
            beforeUpload={file => {
              const maxBytes = imageUploadMaxBytes();
              if (file.size > maxBytes) {
                setUploadFile(null);
                setUploadError('');
                msg.error(imageUploadTooLargeMessage(maxBytes));
                return Upload.LIST_IGNORE;
              }
              setUploadError('');
              setUploadFile(file);
              if (!String(form.getFieldValue('name') || '').trim()) {
                form.setFieldsValue({ name: uploadDefaultName(file.name) });
              }
              return false;
            }}
            maxCount={1}
            fileList={uploadFile ? [{ uid: 'selected-image', name: uploadFile.name, status: 'done' }] : []}
            onRemove={() => { setUploadFile(null); setUploadError(''); return true; }}
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
        {(editing || createMode === 'path') && <Form.Item
          name="file_path"
          label={editing ? '文件路径' : '后端文件路径'}
          tooltip="填写后端服务器上 IMAGE_STORAGE_DIR 内的具体镜像文件；相对路径会按该目录解析。"
          rules={[{ required: true, whitespace: true, message: '请填写后端文件路径' }]}
          preserve={false}
        >
          <Input placeholder="ubuntu-24.04.iso" onChange={() => form.setFields([{ name: 'file_path', errors: [] }])} />
        </Form.Item>}
        <Form.Item name="tags_text" label="启动参数 JSON">
          <Input.TextArea autoSize={{ minRows: 5, maxRows: 10 }} />
        </Form.Item>
        <Form.Item name="sha256" label="SHA256"><Input disabled /></Form.Item>
      </Form>
    </Modal>
  </>;
}
