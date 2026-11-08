// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package registry 模块 #1：注册认证服务
// 流程：register 上行 → 令牌校验（MACS_REGISTER_TOKEN 配置时强制）→ Q1 幂等落库 → ACK 回孵化器 id
package registry

import (
	"encoding/json"
	"errors"
	"net"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

type Service struct {
	store *store.Store
	token string // 空 = 开发模式不校验
}

func New(st *store.Store, token string) *Service { return &Service{store: st, token: token} }

type registerPayload struct {
	Name       string   `json:"name"`
	Capacity   int      `json:"capacity"`
	Platforms  []string `json:"platforms"`
	Drivers    []string `json:"drivers"`
	ListenPort int      `json:"listen_port"`
	Token      string   `json:"token"`
}

// Handle 处理 register 上行，返回 ACK payload
func (s *Service) Handle(env udp.Envelope, src *net.UDPAddr) ([]byte, error) {
	var p registerPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.Name == "" {
		return nil, errors.New("malformed register payload")
	}
	if s.token != "" && p.Token != s.token {
		return nil, errors.New("register token mismatch") // 66100 口径（§6.4.15）
	}
	if p.Capacity <= 0 {
		p.Capacity = 4 // Q4 默认
	}
	platforms, _ := json.Marshal(p.Platforms)
	drivers, _ := json.Marshal(p.Drivers)
	id, err := s.store.UpsertIncubator(store.Incubator{
		ID: udp.NewID(), Name: p.Name, Capacity: p.Capacity,
		Platforms: string(platforms), Drivers: string(drivers),
		ListenPort: p.ListenPort, LastIP: src.IP.String(),
	})
	if err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "incubator_id": id})
	return out, nil
}
