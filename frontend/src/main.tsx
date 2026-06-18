import React, { Suspense, useEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import { App as AntApp, Button, ConfigProvider, Empty, Layout, Menu, Result, Spin, Typography } from 'antd';
import { AlertOutlined, AuditOutlined, CloudUploadOutlined, CodeOutlined, DashboardOutlined, DeploymentUnitOutlined, FileTextOutlined, HddOutlined, LogoutOutlined, SettingOutlined } from '@ant-design/icons';
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom';
import { api, setToken, User } from './api/client';
import { allRoles, hasRole, Role } from './authz';
import { ErrorBoundary } from './components/ErrorBoundary';
import { LoginPage } from './pages/LoginPage';
import './styles.css';

const { Header, Sider, Content } = Layout;

const DashboardPage = React.lazy(() => import('./pages/DashboardPage').then(module => ({ default: module.DashboardPage })));
const ServersPage = React.lazy(() => import('./pages/ServersPage').then(module => ({ default: module.ServersPage })));
const ImagesPage = React.lazy(() => import('./pages/ImagesPage').then(module => ({ default: module.ImagesPage })));
const TemplatesPage = React.lazy(() => import('./pages/TemplatesPage').then(module => ({ default: module.TemplatesPage })));
const DeploymentsPage = React.lazy(() => import('./pages/DeploymentsPage').then(module => ({ default: module.DeploymentsPage })));
const OpsPage = React.lazy(() => import('./pages/OpsPage').then(module => ({ default: module.OpsPage })));
const AlertsPage = React.lazy(() => import('./pages/AlertsPage').then(module => ({ default: module.AlertsPage })));
const SystemPage = React.lazy(() => import('./pages/SystemPage').then(module => ({ default: module.SystemPage })));
const AuditPage = React.lazy(() => import('./pages/AuditPage').then(module => ({ default: module.AuditPage })));

const navItems: Array<{ key: string; icon: React.ReactNode; label: string; roles: Role[] }> = [
  { key: '/dashboard', icon: <DashboardOutlined />, label: '总览', roles: allRoles },
  { key: '/servers', icon: <HddOutlined />, label: '资产管理', roles: allRoles },
  { key: '/images', icon: <CloudUploadOutlined />, label: '镜像管理', roles: allRoles },
  { key: '/templates', icon: <FileTextOutlined />, label: '模板管理', roles: allRoles },
  { key: '/deployments', icon: <DeploymentUnitOutlined />, label: '部署任务', roles: allRoles },
  { key: '/ops', icon: <CodeOutlined />, label: '运维工具', roles: allRoles },
  { key: '/alerts', icon: <AlertOutlined />, label: '监控告警', roles: allRoles },
  { key: '/system', icon: <SettingOutlined />, label: '系统管理', roles: ['admin'] },
  { key: '/audit', icon: <AuditOutlined />, label: '审计日志', roles: allRoles },
];

function RequireRole({ user, roles, children }: { user: User | null; roles: Role[]; children: React.ReactNode }) {
  if (!hasRole(user?.role, roles)) {
    return <Result status="403" title="403" subTitle="当前账号没有访问该页面的权限" />;
  }
  return children;
}

function Shell() {
  const [authed, setAuthed] = useState(Boolean(localStorage.getItem('token')));
  const [user, setUser] = useState<User | null>(null);
  const [loadingMe, setLoadingMe] = useState(Boolean(localStorage.getItem('token')));
  const navigate = useNavigate();
  const location = useLocation();
  const { message } = AntApp.useApp();

  useEffect(() => {
    const token = localStorage.getItem('token');
    setToken(token);
    if (!token) {
      setLoadingMe(false);
      return;
    }
    api.get<User>('/auth/me').then(res => {
      setUser(res.data);
      setAuthed(true);
    }).catch(() => {
      localStorage.removeItem('token');
      setToken(null);
      setAuthed(false);
      setUser(null);
    }).finally(() => setLoadingMe(false));
  }, []);

  useEffect(() => {
    const onForbidden = () => message.warning('当前账号没有权限执行该操作');
    const onAPIError = (event: Event) => message.error((event as CustomEvent<string>).detail || '请求失败，请稍后重试');
    window.addEventListener('api:forbidden', onForbidden);
    window.addEventListener('api:error', onAPIError);
    return () => {
      window.removeEventListener('api:forbidden', onForbidden);
      window.removeEventListener('api:error', onAPIError);
    };
  }, [message]);

  if (loadingMe) return <div className="login-wrap"><Spin size="large" /></div>;
  if (!authed) return <LoginPage onLogin={(nextUser) => { setUser(nextUser); setAuthed(true); navigate('/dashboard', { replace: true }); }} />;
  const logout = () => { localStorage.removeItem('token'); setToken(null); setAuthed(false); setUser(null); navigate('/'); };
  const visibleNavItems = navItems.filter(item => hasRole(user?.role, item.roles));
  return <Layout className="app-shell">
    <Sider className="sidebar" width={244}>
      <Typography.Title level={4} className="brand">裸金属生命周期平台</Typography.Title>
      <Menu theme="dark" mode="inline" selectedKeys={[location.pathname]} onClick={({ key }) => navigate(String(key))} items={visibleNavItems.map(({ key, icon, label }) => ({ key, icon, label }))} />
    </Sider>
    <Layout>
      <Header className="topbar"><Typography.Text>{user?.email} · {user?.role}</Typography.Text><Button icon={<LogoutOutlined />} onClick={logout}>退出</Button></Header>
      <Content className="content"><ErrorBoundary resetKey={location.pathname}><Suspense fallback={<div className="route-loading"><Spin /></div>}><Routes>
            <Route path="/" element={<Navigate to="/dashboard" replace />} />
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/servers" element={<ServersPage role={user?.role} />} />
            <Route path="/images" element={<ImagesPage role={user?.role} />} />
            <Route path="/templates" element={<TemplatesPage role={user?.role} />} />
            <Route path="/deployments" element={<DeploymentsPage role={user?.role} />} />
            <Route path="/ops" element={<OpsPage role={user?.role} />} />
            <Route path="/alerts" element={<AlertsPage role={user?.role} />} />
            <Route path="/system" element={<RequireRole user={user} roles={['admin']}><SystemPage /></RequireRole>} />
            <Route path="/audit" element={<AuditPage />} />
          </Routes></Suspense></ErrorBoundary></Content>
    </Layout>
  </Layout>;
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider renderEmpty={() => <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无数据" />}>
      <AntApp><BrowserRouter><Shell /></BrowserRouter></AntApp>
    </ConfigProvider>
  </React.StrictMode>
);
