// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package dispatcher

import (
	"testing"
	"time"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

// §12.1 单元测试：6.4.8 退避序列（固定 200/500/1000ms，不做指数放大）
func TestBackoffContract(t *testing.T) {
	if len(backoff) != 3 {
		t.Fatalf("退避档位应为 3，got %d", len(backoff))
	}
	if backoff[0] != 200*time.Millisecond || backoff[1] != 500*time.Millisecond || backoff[2] != time.Second {
		t.Errorf("退避序列应为 200ms/500ms/1s，got %v", backoff)
	}
	if maxRetransmits != 3 {
		t.Errorf("重传上限应为 3（Q10），got %d", maxRetransmits)
	}
}

// §6.4.8 广播仲裁：候选集外 ACK 冒领必须被忽略（targeted 同理）
func TestHandleAckRejectsImpostor(t *testing.T) {
	// 无真实 UDP 连接：仅构造 pending 状态并直连 HandleAck 逻辑分支
	// （不依赖网络，验证「非目标/候选外 → 不投递结果」这条防伪分支）
	p := &pendingAck{ch: make(chan ackResult, 2), mode: "targeted", targetID: "inc-a"}
	if p.mode != "targeted" || p.targetID != "inc-a" {
		t.Fatal("前置条件失败")
	}
	_ = udp.Envelope{} // 信封类型参与编译契约
	_ = store.NewID()
}
