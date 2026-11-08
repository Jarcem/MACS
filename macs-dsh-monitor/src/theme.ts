// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// HUD 主题 token → Ink 颜色映射（UI 设计 V1.3 §15.5）
// Ink 不支持 alpha，dim 用 gray 近似；状态符号语义沿用 §14.7
export const hud = {
  cyan: '#00e5ff',
  green: '#00ff9c',
  amber: '#ffb454',
  red: '#ff5470',
  violet: '#b18cff',
  text: '#d6f4ff',
  dim: 'gray',
} as const;

// 状态 → 颜色（语义与 §14.7 七色映射一致）
export const statusColor: Record<string, string> = {
  running: hud.green,
  completed: hud.cyan,
  accepted: hud.cyan,
  failed: hud.red,
  frozen: hud.dim,
  pending: hud.dim,
  self_checking: hud.amber,
  reviewing: hud.violet,
  dispatched: hud.dim,
  merging: hud.violet,
};
