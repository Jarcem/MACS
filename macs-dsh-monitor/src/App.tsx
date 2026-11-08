// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

import React, { useState } from 'react';
import { Box, Text, useApp, useInput } from 'ink';
import {
  AdaptersView,
  ClusterView,
  DispatchView,
  HistoryView,
  OverviewView,
  PlatformsView,
  TasksView,
  TelemetryView,
} from './views.js';
import { TaskBlueprintView } from './taskView.js';
import { hud } from './theme.js';

// 视图注册表：与 macs-tower-web 页面清单（UI 设计 V1.2 附录 A）对齐的终端版
const VIEWS = [
  { key: '1', name: '运行总览', render: () => <OverviewView /> },
  { key: '2', name: '集群拓扑', render: () => <ClusterView /> },
  { key: '3', name: '调度监控', render: () => <DispatchView /> },
  { key: '4', name: '遥测', render: () => <TelemetryView /> },
  { key: '5', name: '适配器', render: () => <AdaptersView /> },
  { key: '6', name: '平台管理', render: () => <PlatformsView /> },
  { key: '7', name: '任务', render: () => <TasksView /> },
  { key: '8', name: '任务透视', render: () => <TaskBlueprintView /> },
  { key: '9', name: '历史记录', render: () => <HistoryView /> },
] as const;

export function App() {
  const [active, setActive] = useState(0);
  const { exit } = useApp();

  useInput((input, key) => {
    const idx = VIEWS.findIndex((v) => v.key === input);
    if (idx >= 0) setActive(idx);
    if (key.leftArrow) setActive((a) => (a - 1 + VIEWS.length) % VIEWS.length);
    if (key.rightArrow) setActive((a) => (a + 1) % VIEWS.length);
    if (input === 'q' || key.escape) exit();
  });

  return (
    <Box flexDirection="column">
      <Box gap={1}>
        {VIEWS.map((v, i) => (
          <Text key={v.key} bold={i === active} color={i === active ? hud.green : hud.dim}>
            [{v.key}]{v.name}
          </Text>
        ))}
        <Text dimColor>  ←/→ 切换 · q 退出</Text>
      </Box>
      <Box marginTop={1}>{VIEWS[active].render()}</Box>
    </Box>
  );
}
