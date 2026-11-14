// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 审批/告警出站通知（§6.4.17 C9：webhook 单通道先行，企微/SMTP 预留）
package approvals

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"macs.local/tower/internal/store"
)

// postWebhook 向单个 webhook 通道投递告警（fire-and-forget，失败仅记日志）
func postWebhook(ch store.NotifyChannel, payload map[string]any) {
	var cfg struct {
		URL string `json:"url"`
	}
	if ch.Kind != "webhook" || json.Unmarshal([]byte(ch.Config), &cfg) != nil || cfg.URL == "" {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(cfg.URL, "application/json", bytes.NewReader(b))
	if err != nil {
		log.Printf(`{"level":"warn","module":"notify","msg":"webhook post failed: %v"}`, err)
		return
	}
	resp.Body.Close()
}
