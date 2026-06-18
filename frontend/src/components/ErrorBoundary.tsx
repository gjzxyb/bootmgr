import React from 'react';
import { Button, Result } from 'antd';

type Props = {
  children: React.ReactNode;
  resetKey?: string;
};

type State = {
  error: Error | null;
};

export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('route render failed', error, info);
  }

  componentDidUpdate(prevProps: Props) {
    if (prevProps.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    if (!this.state.error) return this.props.children;
    return <Result
      status="500"
      title="页面加载失败"
      subTitle="当前页面渲染失败，可以重试或返回总览。"
      extra={[
        <Button key="reload" type="primary" onClick={() => window.location.reload()}>重新加载</Button>,
        <Button key="dashboard" onClick={() => window.location.assign('/dashboard')}>返回总览</Button>,
      ]}
    />;
  }
}
