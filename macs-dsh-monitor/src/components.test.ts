// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// ASCII 进度条（UI §16.1 终端渲染规范：█ 已完 / ░ 未完）
import { describe, expect, it } from 'vitest';
import { bar } from './components.js';

describe('ASCII 进度条（§16.1）', () => {
  it('0% 全未完', () => {
    expect(bar(0, 4)).toBe('░░░░');
  });
  it('100% 全完', () => {
    expect(bar(100, 4)).toBe('████');
  });
  it('50% 半完', () => {
    expect(bar(50, 4)).toBe('██░░');
  });
});
