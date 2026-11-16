// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 最小冒烟测试：状态色映射（UI §8.2 五档语义）——不渲染组件，验证纯函数语义
import { describe, expect, it } from 'vitest';

// 从 App.tsx 提取的映射逻辑副本（枚举字典驱动 + 回退内置；与实现同源维护）
const FALLBACK_COLOR: Record<string, string[]> = {
  success: ['online', 'acked', 'completed', 'passed', 'approved', 'ready', 'spot_check_passed'],
  active: ['running', 'reviewing', 'in_spot_check'],
  neutral: ['pending', 'registered', 'skipped'],
  waiting: ['dispatched', 'review_pending', 'self_checking'],
  warn: ['overloaded', 'retransmitted', 'partial_failure', 'timeout'],
  danger: ['offline', 'failed', 'rejected', 'blocked', 'frozen', 'revoked', 'unreachable'],
};
const ENUM_COLOR: Record<string, string> | null = null;
const statusClass = (s: string) => {
  const color = ENUM_COLOR?.[s] ?? (Object.entries(FALLBACK_COLOR).find(([, list]) => list.includes(s))?.[0] ?? 'neutral');
  return ({ success: 'hud-completed', active: 'hud-running', waiting: 'hud-checking', warn: 'hud-warn', danger: 'hud-failed' } as Record<string, string>)[color] ?? 'hud-pending';
};

describe('statusClass 状态色语义（UI §8.2）', () => {
  it('成功态 → hud-completed', () => {
    expect(statusClass('completed')).toBe('hud-completed');
    expect(statusClass('online')).toBe('hud-completed');
  });
  it('进行中 → hud-running', () => {
    expect(statusClass('running')).toBe('hud-running');
    expect(statusClass('in_spot_check')).toBe('hud-running');
  });
  it('警告档（V3.5.5 琥珀）→ hud-warn', () => {
    expect(statusClass('overloaded')).toBe('hud-warn');
    expect(statusClass('partial_failure')).toBe('hud-warn');
    expect(statusClass('timeout')).toBe('hud-warn');
  });
  it('危险档 → hud-failed', () => {
    expect(statusClass('failed')).toBe('hud-failed');
    expect(statusClass('frozen')).toBe('hud-failed');
  });
  it('未知状态回退中性 → hud-pending', () => {
    expect(statusClass('brand-new-state')).toBe('hud-pending');
  });
});
