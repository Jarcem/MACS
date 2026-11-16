// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 最小冒烟测试：HUD 主题令牌（§15 蓝绿终端 HUD）
import { describe, expect, it } from 'vitest';
import { hud } from './theme.js';

describe('HUD 主题令牌', () => {
  it('包含核心状态色', () => {
    expect(hud).toHaveProperty('green');
    expect(hud).toHaveProperty('red');
    expect(hud).toHaveProperty('dim');
  });
});
