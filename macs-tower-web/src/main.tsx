import React from 'react';
import ReactDOM from 'react-dom/client';
import './styles/hud.css'; // HUD 主题（UI 设计 V1.3 §15）
import { App } from './App';

// MACS 管理后台入口（模块 #27，UI 设计 V1.1 三平面）
// M1 已落地：运行总览 / 集群拓扑 / 调度监控；其余页面按附录 A 随里程碑补齐
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
