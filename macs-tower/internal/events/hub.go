// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package events 进程内事件总线：编排器发布，SSE / 监控端订阅（慢消费者丢帧，不阻塞调度）
package events

import (
	"encoding/json"
	"sync"
	"time"
)

type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func New() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

// Publish 广播一条事件（非阻塞）
func (h *Hub) Publish(event string, data any) {
	b, err := json.Marshal(map[string]any{
		"event": event,
		"ts":    time.Now().UTC().Format(time.RFC3339),
		"data":  data,
	})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // 订阅端积压超过缓冲则丢弃该帧
		}
	}
}

// Subscribe 返回事件通道与取消函数
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		close(ch)
	}
}
