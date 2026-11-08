// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 第三方包类型补充：eventsource 无官方类型声明
declare module 'eventsource' {
  interface EventSourceInit {
    headers?: Record<string, string>;
  }
  class EventSource {
    constructor(url: string, init?: EventSourceInit);
    addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
    close(): void;
  }
  export default EventSource;
}
