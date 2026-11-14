// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package authz §6.4.16 令牌与权限模型：Admin Token 三级 scope（read/ops/danger）
//   read   监控只读（全部 GET）
//   ops    任务创建、审批决策、模板增改
//   danger cmd_exec 签发、密钥轮换、Admin Token 签发、模板删除、预算上界
// 引导口径：MACS_ADMIN_TOKEN 环境变量视为 danger 引导令牌；两者皆空 = 开发模式放行（README Quickstart 兼容）
package authz

import (
	"net/http"
	"strings"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/trace"
)

const (
	ScopeRead   = "read"
	ScopeOps    = "ops"
	ScopeDanger = "danger"
)

var scopeRank = map[string]int{ScopeRead: 0, ScopeOps: 1, ScopeDanger: 2}

type Enforcer struct {
	st          *store.Store
	bootstrap   string // MACS_ADMIN_TOKEN（danger）
	hasDBTokens bool
}

func New(st *store.Store, bootstrap string) *Enforcer {
	return &Enforcer{st: st, bootstrap: bootstrap}
}

// Refresh 重新加载 DB 令牌存在性（签发/吊销后调用）
func (e *Enforcer) Refresh() {
	has, _ := e.st.HasAnyAdminToken()
	e.hasDBTokens = has
}

// Enabled 是否启用鉴权（未配置任何令牌 = 开发模式放行）
func (e *Enforcer) Enabled() bool { return e.bootstrap != "" || e.hasDBTokens }

// tokenScope 解析 Bearer 令牌 → scope（找不到返回 ""）
func (e *Enforcer) tokenScope(token string) string {
	if token == "" {
		return ""
	}
	if e.bootstrap != "" && token == e.bootstrap {
		return ScopeDanger
	}
	t, valid, _ := e.st.FindAdminToken(trace.HashHex(token))
	if valid {
		return t.Scope
	}
	return ""
}

// Middleware 按 required scope 校验；开发模式全放行
func (e *Enforcer) Middleware(required string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.Enabled() {
			next(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		scope := e.tokenScope(token)
		if scopeRank[scope] >= scopeRank[required] && scope != "" {
			next(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":66100,"message":"unauthorized: requires ` + required + ` scope","details":{}}`))
	}
}
