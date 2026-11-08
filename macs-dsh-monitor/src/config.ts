// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 监控终端配置：数据源指向 macs-tower 管理后台 API（只读通道）
// 端口口径：HTTPS 管理端口默认 8471（主设计附录 A · Q9）
export const config = {
  apiBase: process.env.MACS_TOWER_API ?? 'http://127.0.0.1:8471',
  adminToken: process.env.MACS_ADMIN_TOKEN ?? '',
  refreshMs: Number(process.env.MACS_MONITOR_REFRESH_MS ?? 5000),
};
