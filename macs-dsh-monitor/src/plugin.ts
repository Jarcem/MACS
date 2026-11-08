// deepseek harness 插件入口
// DSH 加载本插件后，可通过 harness 命令唤起 MACS 监控终端（内容同管理后台 web 端）
export const plugin = {
  name: 'macs-monitor',
  version: '0.1.0',
  description: 'MACS 监控终端（三平面只读视图，数据源 /api/v1 REST + SSE）',
  commands: ['macs-monitor'],
  async activate(): Promise<void> {
    const [{ render }, React, { App }] = await Promise.all([
      import('ink'),
      import('react'),
      import('./App.js'),
    ]);
    render(React.createElement(App));
  },
};

export default plugin;
