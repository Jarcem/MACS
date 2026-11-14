// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package dispatcher 模块 #3：广播调度服务
// ACK 契约唯一事实源 = 主设计 §6.4.8：
//   固定退避 200ms/500ms/1s，最多重传 3 次（总确认窗口 1.7s、超时阈值 2s）
//   广播任务分配 first-ACK-wins：首个合法 ACK 锁定承接者，其余候选广播 dispatch_commit；
//   已重复孵化的候选凭 worker_rollback 幂等回滚（E16）；同刻竞态按 incubator_id 字典序仲裁
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

// 6.4.8 唯一口径：BACKOFF 固定不做指数放大（内网 RTT 极低、丢包多为瞬时突发）
var backoff = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second}

const (
	ackTotalWindow = 2 * time.Second // 超时阈值（含调度与时钟容差）
	maxRetransmits = 3               // 重传上限 3 次（首传 + 3 重传）
)

// ErrDriverMismatch 能力不满足（§6.4.13.6：不计 ACK 失败，改选候选）
var ErrDriverMismatch = errors.New("driver mismatch")

type ackResult struct {
	err error // nil=accepted；ErrDriverMismatch=改派；其余=拒绝
}

type pendingAck struct {
	ch       chan ackResult
	mode     string            // targeted/broadcast
	targetID string            // targeted 目标校验（防冒领）
	cands    map[string]string // broadcast 候选集 id→addr

	mu     sync.Mutex
	locked bool
	winner string
}

type Dispatcher struct {
	store    *store.Store
	conn     *net.UDPConn
	pending  sync.Map // msg_id → *pendingAck
	breakers sync.Map // incubator_id → *breaker（6.4.10 上游断路器）
}

// breaker 断路器：同一孵化器连续 5 次下发失败 → 熔断 60s（半开探测由过期自动恢复实现）
type breaker struct {
	fails     int
	openUntil time.Time
}

const (
	breakerThreshold = 5
	breakerOpenFor   = 60 * time.Second
)

// CircuitOpen 熔断期内该节点不承接新调度（等价临时下线，orchestrator 选机过滤）
func (d *Dispatcher) CircuitOpen(incubatorID string) bool {
	if d == nil {
		return false // 测试裸构造 Orchestrator 时 nil 安全
	}
	v, ok := d.breakers.Load(incubatorID)
	if !ok {
		return false
	}
	b := v.(*breaker)
	if time.Now().Before(b.openUntil) {
		return true
	}
	// 半开：熔断过期后清零，允许探测
	d.breakers.Delete(incubatorID)
	return false
}

func (d *Dispatcher) noteFailure(incubatorID string) {
	v, _ := d.breakers.LoadOrStore(incubatorID, &breaker{})
	b := v.(*breaker)
	b.fails++
	if b.fails >= breakerThreshold && time.Now().After(b.openUntil) {
		b.openUntil = time.Now().Add(breakerOpenFor)
		_ = d.store.InsertGuardrailEvent(store.GuardrailEvent{
			ID: store.NewID(), IncubatorID: incubatorID,
			Type: "circuit_breaker", Action: "paused",
			Detail: fmt.Sprintf(`{"consecutive_failures":%d,"open_for_sec":%d}`, b.fails, int(breakerOpenFor.Seconds())),
		})
	}
}

func (d *Dispatcher) noteSuccess(incubatorID string) {
	d.breakers.Delete(incubatorID)
}

func New(st *store.Store, conn *net.UDPConn) *Dispatcher {
	return &Dispatcher{store: st, conn: conn}
}

// addrOf 候选孵化器 UDP 地址
func addrOf(i *store.Incubator) (*net.UDPAddr, error) {
	if i.ListenPort <= 0 || i.LastIP == "" {
		return nil, errors.New("incubator unreachable (no addr)")
	}
	return &net.UDPAddr{IP: net.ParseIP(i.LastIP), Port: i.ListenPort}, nil
}

// sendPacket 封包发送（单播；重传共用同一 msg_id，孵化器按 dispatch_inbox 幂等去重并重 ACK）
func (d *Dispatcher) sendPacket(msgID, subtaskID string, taskDef map[string]any, dest *net.UDPAddr) error {
	env, err := udp.New(udp.MsgDispatch, map[string]any{
		"subtask_id": subtaskID, "task_def": taskDef,
	})
	if err != nil {
		return err
	}
	env.MsgID = msgID
	b, _ := json.Marshal(env)
	_, err = d.conn.WriteToUDP(b, dest)
	return err
}

// NotifyIncubator 向指定孵化器下发通用通知（6.4.3 下行指令全集对外出口：
// task_cancelled / approval_result / template_updated / budget_changed 等）
func (d *Dispatcher) NotifyIncubator(incubatorID, msgType string, payload map[string]any) {
	if d == nil || d.conn == nil {
		return // 测试/无网络环境
	}
	inc, err := d.store.GetIncubator(incubatorID)
	if err != nil {
		return
	}
	if dest, err := addrOf(&inc); err == nil {
		d.sendNotice(msgType, payload, dest)
	}
}

// BroadcastNotice 广播通知全部在线孵化器（template_updated / config_updated 等）
func (d *Dispatcher) BroadcastNotice(msgType string, payload map[string]any) {
	if d == nil || d.conn == nil {
		return
	}
	list, err := d.store.ListIncubators(true)
	if err != nil {
		return
	}
	for i := range list {
		if dest, err := addrOf(&list[i]); err == nil {
			d.sendNotice(msgType, payload, dest)
		}
	}
}

// sendNotice 下行通用通知（dispatch_commit / worker_rollback / budget_changed 等；6.4.3 指令全集）
func (d *Dispatcher) sendNotice(msgType string, payload map[string]any, dest *net.UDPAddr) {
	env, err := udp.New(msgType, payload)
	if err != nil {
		return
	}
	b, _ := json.Marshal(env)
	_, _ = d.conn.WriteToUDP(b, dest)
}

// HandleAck 网关路由入口：校验 → 仲裁 → 落库（6.4.8 onAck 伪代码）
func (d *Dispatcher) HandleAck(env udp.Envelope) {
	v, ok := d.pending.Load(env.InReplyTo)
	if !ok {
		return // 未匹配 → duplicate，忽略（E16）
	}
	p := v.(*pendingAck)

	var body struct {
		OK             bool   `json:"ok"`
		IncubatorID    string `json:"incubator_id"`
		Mismatch       bool   `json:"driver_mismatch"`
		AcceptedSubID  string `json:"accepted_subtask_id"`
		AvailableSlots int    `json:"available_slots"`
	}
	_ = json.Unmarshal(env.Payload, &body)

	if p.mode == "targeted" {
		// 安全防伪：只接受目标孵化器的 ACK
		if body.IncubatorID != "" && body.IncubatorID != p.targetID {
			return
		}
		if body.Mismatch {
			d.store.SetDispatchAck(env.InReplyTo, "driver_mismatch") // 不计 ACK 失败
			p.ch <- ackResult{err: ErrDriverMismatch}
			return
		}
		if body.OK {
			d.store.SetDispatchAck(env.InReplyTo, "acked")
			p.ch <- ackResult{}
		}
		return
	}

	// broadcast：候选集外忽略（防第三方冒领）；能力不符的候选继续等待其他 ACK
	if body.IncubatorID == "" {
		return
	}
	if _, inCands := p.cands[body.IncubatorID]; !inCands {
		return
	}
	if body.Mismatch || !body.OK {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.locked {
		p.locked = true // 首个合法 ACK 锁定承接者（first-ACK-wins）
		p.winner = body.IncubatorID
		d.store.SetDispatchAck(env.InReplyTo, "acked")
		p.ch <- ackResult{}
		// 锁定后向其余候选广播 dispatch_commit：停止应答、不孵化
		go d.commitOthers(env.InReplyTo, body.AcceptedSubID, body.IncubatorID, p.cands)
		return
	}
	// 后到 ACK → duplicate；若已重复孵化则幂等回滚（E16）
	d.store.SetDispatchAck(env.InReplyTo, "duplicate")
	if body.AcceptedSubID != "" {
		go d.sendWorkerRollback(body.IncubatorID, body.AcceptedSubID)
	}
}

// commitOthers 其余候选停止应答（6.4.3 dispatch_commit 下行指令）
func (d *Dispatcher) commitOthers(msgID, subtaskID, winnerID string, cands map[string]string) {
	for id, addr := range cands {
		if id == winnerID {
			continue
		}
		dest, err := net.ResolveUDPAddr("udp4", addr)
		if err != nil {
			continue
		}
		d.sendNotice(udp.MsgDispatchCommit, map[string]any{
			"ack_for": msgID, "subtask_id": subtaskID, "incubator_id": winnerID,
		}, dest)
	}
}

// sendWorkerRollback 重复孵化兜底（E16）：被回滚方幂等销毁
func (d *Dispatcher) sendWorkerRollback(incubatorID, subtaskID string) {
	inc, err := d.store.GetIncubator(incubatorID)
	if err != nil {
		return
	}
	dest, err := addrOf(&inc)
	if err != nil {
		return
	}
	d.sendNotice(udp.MsgWorkerRollback, map[string]any{"subtask_id": subtaskID}, dest)
}

// waitAck 等待已登记通道的 ACK（登记由调用方在发包前完成，避免 ACK 先到竞态）
func (d *Dispatcher) waitAck(ctx context.Context, p *pendingAck, timeout time.Duration) error {
	select {
	case r := <-p.ch:
		return r.err
	case <-time.After(timeout):
		return errors.New("ack timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DispatchTo 定向下发（6.4.8 sendWithAck）：首传 + 3 重传，固定退避；同一 msg_id 贯穿全程
func (d *Dispatcher) DispatchTo(ctx context.Context, target *store.Incubator, subtaskID string, taskDef map[string]any) (string, error) {
	dest, err := addrOf(target)
	if err != nil {
		return "", err
	}
	msgID := udp.NewID()
	if err := d.store.InsertDispatchLog(store.NewID(), subtaskID, target.ID, msgID, "targeted"); err != nil {
		return "", err
	}

	// 先登记 pending 再发包：localhost/低延迟下 ACK 可能早于登记到达（否则必然假超时）
	p := &pendingAck{ch: make(chan ackResult, 4), mode: "targeted", targetID: target.ID}
	d.pending.Store(msgID, p)
	defer d.pending.Delete(msgID)

	for attempt := 0; attempt <= maxRetransmits; attempt++ {
		if err := d.sendPacket(msgID, subtaskID, taskDef, dest); err != nil {
			d.store.SetDispatchAck(msgID, "failed")
			return "", err
		}
		if attempt == 0 {
			d.store.SetDispatchAck(msgID, "sent")
		} else {
			d.store.BumpDispatchRetransmit(msgID)
		}
		wait := ackTotalWindow
		if attempt < len(backoff) {
			wait = backoff[attempt]
		}
		if err := d.waitAck(ctx, p, wait); err == nil {
			d.noteSuccess(target.ID)
			return target.ID, nil
		} else if errors.Is(err, ErrDriverMismatch) {
			d.store.SetDispatchAck(msgID, "driver_mismatch")
			return "", err // 改派：候选下一个
		}
	}
	d.store.SetDispatchAck(msgID, "failed")
	d.noteFailure(target.ID) // 6.4.10：连续失败累计，达阈值熔断 60s
	return "", fmt.Errorf("dispatch timeout after %d retransmits (subtask=%s target=%s)", maxRetransmits, subtaskID, target.Name)
}

// DispatchBroadcast 广播下发（6.4.7 → 6.4.8）：候选集同包广播，first-ACK-wins 承接
// 返回承接者孵化器 ID；全部候选未确认 → 候选整体回池（由编排器按失败重试处理）
func (d *Dispatcher) DispatchBroadcast(ctx context.Context, candidates []*store.Incubator, subtaskID string, taskDef map[string]any) (string, error) {
	if len(candidates) == 0 {
		return "", errors.New("no candidates")
	}
	if len(candidates) == 1 {
		return d.DispatchTo(ctx, candidates[0], subtaskID, taskDef)
	}

	msgID := udp.NewID()
	first := candidates[0]
	if err := d.store.InsertDispatchLog(store.NewID(), subtaskID, first.ID, msgID, "broadcast"); err != nil {
		return "", err
	}

	cands := map[string]string{}
	for _, c := range candidates {
		if a, err := addrOf(c); err == nil {
			cands[c.ID] = a.String()
		}
	}
	p := &pendingAck{ch: make(chan ackResult, len(cands)+1), mode: "broadcast", cands: cands}
	d.pending.Store(msgID, p)
	defer d.pending.Delete(msgID)

	sent := 0
	for _, c := range candidates {
		dest, err := addrOf(c)
		if err != nil {
			continue
		}
		if err := d.sendPacket(msgID, subtaskID, taskDef, dest); err != nil {
			continue
		}
		sent++
	}
	if sent == 0 {
		d.store.SetDispatchAck(msgID, "failed")
		return "", errors.New("broadcast send failed")
	}
	d.store.SetDispatchAck(msgID, "sent")

	// 收集窗口：首个合法 ACK 锁定承接（HandleAck 置 locked）；窗口耗尽未锁定 → 失败
	select {
	case r := <-p.ch:
		if r.err != nil {
			d.store.SetDispatchAck(msgID, "failed")
			return "", r.err
		}
		p.mu.Lock()
		winner := p.winner
		p.mu.Unlock()
		d.noteSuccess(winner)
		return winner, nil
	case <-time.After(ackTotalWindow):
		d.store.SetDispatchAck(msgID, "failed")
		for id := range cands {
			d.noteFailure(id) // 全体候选未确认，均计入连续失败
		}
		return "", fmt.Errorf("broadcast ack window exhausted (subtask=%s, candidates=%d)", subtaskID, len(cands))
	}
}

// StartJanitor ACK 兜底扫描（6.5 ackRetransmitTick）：塔重启后卡在 sent/retransmitted 的行标记 failed（E13），
// 其子任务若仍处 dispatched 则回池 pending（保留重试计数），由编排器 2s tick 重派。
// 迟到的执行结果由 HandleResult 幂等吸收（终态重复回传不重复生效）。
func (d *Dispatcher) StartJanitor() {
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for range t.C {
			cutoff := time.Now().UTC().Add(-2 * ackTotalWindow).Format(time.RFC3339)
			rows, err := d.store.ListStuckDispatchRows(cutoff)
			if err != nil {
				continue
			}
			for _, r := range rows {
				d.store.SetDispatchAck(r.MsgID, "failed")
				if sub, err := d.store.GetSubtask(r.SubtaskID); err == nil && sub.Status == "dispatched" {
					_ = d.store.SetSubtaskStatus(r.SubtaskID, "pending", "")
				}
			}
		}
	}()
}

// Dispatch M1 调度测试钩子：选首台在线孵化器定向下发
func (d *Dispatcher) Dispatch(ctx context.Context, subtaskID string, taskDef map[string]any) (string, error) {
	list, err := d.store.ListIncubators(true)
	if err != nil {
		return "", err
	}
	var target *store.Incubator
	for i := range list {
		if list[i].ListenPort > 0 && list[i].LastIP != "" {
			target = &list[i]
			break
		}
	}
	if target == nil {
		return "", errors.New("no online incubator")
	}
	return d.DispatchTo(ctx, target, subtaskID, taskDef)
}

// NoticeBudgetChanged 预算变更下行（6.4.11 budget_changed：limit_version 仅收紧不放宽）
func (d *Dispatcher) NoticeBudgetChanged(incubatorID string, payload map[string]any) {
	if d == nil || d.conn == nil {
		return // 测试/无网络环境
	}
	inc, err := d.store.GetIncubator(incubatorID)
	if err != nil {
		return
	}
	if dest, err := addrOf(&inc); err == nil {
		d.sendNotice(udp.MsgBudgetChanged, payload, dest)
	}
}
