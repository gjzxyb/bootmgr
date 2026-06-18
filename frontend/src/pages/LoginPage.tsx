import { useState } from 'react';
import axios from 'axios';
import { Button, Form, Input, Typography, message } from 'antd';
import { api, setToken, User } from '../api/client';

export function LoginPage({ onLogin }: { onLogin: (user: User) => void }) {
  const [msg, holder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const showDemoCredentials = import.meta.env.VITE_SHOW_DEMO_CREDENTIALS !== 'false';
  const submit = async (values: { email: string; password: string }) => {
    setSubmitting(true);
    try {
      const { data } = await api.post('/auth/login', values);
      localStorage.setItem('token', data.token);
      setToken(data.token);
      onLogin(data.user);
    } catch (error) {
      if (axios.isAxiosError(error) && error.response?.status === 429) {
        const retryAfter = error.response.headers['retry-after'];
        msg.error(retryAfter ? `登录尝试过多，请 ${retryAfter} 秒后再试` : '登录尝试过多，请稍后再试');
      } else if (axios.isAxiosError(error) && !error.response) {
        msg.error('无法连接后端服务，请检查 API 是否已启动');
      } else {
        msg.error('登录失败，请检查账号密码');
      }
    } finally {
      setSubmitting(false);
    }
  };
  return <div className="login-wrap">{holder}<div className="login-panel">
    <Typography.Title level={3}>裸金属生命周期平台</Typography.Title>
    {showDemoCredentials && <Typography.Paragraph type="secondary">默认账号 admin@example.com / Admin@123456</Typography.Paragraph>}
    <Form layout="vertical" onFinish={submit} initialValues={showDemoCredentials ? { email: 'admin@example.com', password: 'Admin@123456' } : undefined}>
      <Form.Item label="邮箱" name="email" rules={[{ required: true }]}><Input /></Form.Item>
      <Form.Item label="密码" name="password" rules={[{ required: true }]}><Input.Password /></Form.Item>
      <Button block type="primary" htmlType="submit" loading={submitting}>登录</Button>
    </Form>
  </div></div>;
}
