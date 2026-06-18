import { AlertOutlined, AuditOutlined, CloudServerOutlined, DeploymentUnitOutlined, FileImageOutlined } from '@ant-design/icons';
import { Card, Col, Row, Space, Statistic, Table, Tag, Typography } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import { api, AuditLog, Deployment } from '../api/client';

type DashboardData = {
  servers: number;
  images: number;
  deployments: number;
  active_alerts: number;
  audit_logs: number;
  server_statuses: Record<string, number>;
  deployment_statuses: Record<string, number>;
  alert_severities: Record<string, number>;
  recent_deployments: Deployment[];
  recent_audit_logs: AuditLog[];
};

const initialData: DashboardData = {
  servers: 0,
  images: 0,
  deployments: 0,
  active_alerts: 0,
  audit_logs: 0,
  server_statuses: {},
  deployment_statuses: {},
  alert_severities: {},
  recent_deployments: [],
  recent_audit_logs: []
};

const statusColor = (status: string) => {
  if (['success', 'ready', 'running', 'active'].includes(status)) return 'green';
  if (['pending', 'deploying', 'acknowledged'].includes(status)) return 'blue';
  if (['failed', 'critical', 'firing'].includes(status)) return 'red';
  if (['cancelled', 'retired', 'warning'].includes(status)) return 'orange';
  return 'default';
};

const rowsFrom = (source: Record<string, number>) => Object.entries(source || {}).map(([name, count]) => ({ name, count }));

export function DashboardPage() {
  const [data, setData] = useState<DashboardData>(initialData);
  useEffect(() => { api.get('/dashboard').then(r => setData({ ...initialData, ...r.data })); }, []);

  const serverRows = useMemo(() => rowsFrom(data.server_statuses), [data.server_statuses]);
  const deploymentRows = useMemo(() => rowsFrom(data.deployment_statuses), [data.deployment_statuses]);
  const alertRows = useMemo(() => rowsFrom(data.alert_severities), [data.alert_severities]);

  const breakdownColumns = [
    { title: '状态', dataIndex: 'name', render: (v: string) => <Tag color={statusColor(v)}>{v}</Tag> },
    { title: '数量', dataIndex: 'count', align: 'right' as const }
  ];

  return <>
    <Typography.Title level={3} className="page-title">总览</Typography.Title>
    <Row gutter={[16, 16]}>
      <Col xs={24} md={8} xl={4}><Card><Statistic title="服务器资产" value={data.servers} prefix={<CloudServerOutlined />} /></Card></Col>
      <Col xs={24} md={8} xl={4}><Card><Statistic title="镜像数量" value={data.images} prefix={<FileImageOutlined />} /></Card></Col>
      <Col xs={24} md={8} xl={4}><Card><Statistic title="部署任务" value={data.deployments} prefix={<DeploymentUnitOutlined />} /></Card></Col>
      <Col xs={24} md={8} xl={4}><Card><Statistic title="活跃告警" value={data.active_alerts} prefix={<AlertOutlined />} valueStyle={{ color: data.active_alerts ? '#cf1322' : undefined }} /></Card></Col>
      <Col xs={24} md={8} xl={4}><Card><Statistic title="审计记录" value={data.audit_logs} prefix={<AuditOutlined />} /></Card></Col>
    </Row>

    <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
      <Col xs={24} lg={8}><Card title="资产状态"><Table rowKey="name" size="small" dataSource={serverRows} columns={breakdownColumns} pagination={false} /></Card></Col>
      <Col xs={24} lg={8}><Card title="部署状态"><Table rowKey="name" size="small" dataSource={deploymentRows} columns={breakdownColumns} pagination={false} /></Card></Col>
      <Col xs={24} lg={8}><Card title="告警级别"><Table rowKey="name" size="small" dataSource={alertRows} columns={breakdownColumns} pagination={false} /></Card></Col>
    </Row>

    <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
      <Col xs={24} xl={12}>
        <Card title="最近部署">
          <Table rowKey="id" size="small" dataSource={data.recent_deployments} pagination={false} columns={[
            { title: 'ID', dataIndex: 'id', width: 80 },
            { title: '服务器', dataIndex: 'server_id' },
            { title: '镜像', dataIndex: 'image_id' },
            { title: '状态', dataIndex: 'status', render: v => <Tag color={statusColor(v)}>{v}</Tag> },
            { title: '创建时间', dataIndex: 'created_at' }
          ]} />
        </Card>
      </Col>
      <Col xs={24} xl={12}>
        <Card title="最近操作">
          <Table rowKey="id" size="small" dataSource={data.recent_audit_logs} pagination={false} columns={[
            { title: '时间', dataIndex: 'created_at' },
            { title: '用户', dataIndex: 'actor_email' },
            { title: '动作', dataIndex: 'action' },
            { title: '资源', render: (_, r) => <Space size={4}><span>{r.resource_type}</span><span>{r.resource_id}</span></Space> },
            { title: '风险', dataIndex: 'risk_level', render: v => <Tag color={statusColor(v)}>{v}</Tag> }
          ]} />
        </Card>
      </Col>
    </Row>
  </>;
}
