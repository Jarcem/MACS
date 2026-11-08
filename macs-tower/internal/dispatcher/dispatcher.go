// Package dispatcher 模块 #3：广播调度服务
// Dispatch：选在线孵化器 → 单播 dispatch → 等 ACK（3s）→ 指数退避重试 ≤3 次 → dispatch_log 落终态
// ACK 等待通过 pending 相关表由网关路由 HandleAck 唤醒
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

const (
	ackTimeout  = 3 * time.Second
	maxAttempts = 3
)

type Dispatcher struct {
	store   *store.Store
	conn    *net.UDPConn // 复用网关 socket 发包
	pending sync.Map    // msg_id → chan error
}

func New(st *store.Store, conn *net.UDPConn) *Dispatcher {
	return &Dispatcher{store: st, conn: conn}
}

// HandleAck 网关路由入口：ACK 的 in_reply_to 关联原 dispatch msg_id
func (d *Dispatcher) HandleAck(env udp.Envelope) {
	if ch, ok := d.pending.LoadAndDelete(env.InReplyTo); ok {
		var p struct {
			OK      bool   `json:"ok"`
			Mismatch bool  `json:"driver_mismatch"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if p.Mismatch {
			d.store.SetDispatchAck(env.InReplyTo, "driver_mismatch") // §6.4.13.6：不计 ACK 失败
		} else if p.OK {
			d.store.SetDispatchAck(env.InReplyTo, "acked")
		}
		ch.(chan error) <- nil
	}
}

// Dispatch 下发一个子任务；M1 选首台在线孵化器（M2 编排器改用 DispatchTo 指定目标）
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

// DispatchTo 向指定孵化器单播 dispatch，等 ACK，失败指数退避重试 ≤3 次
func (d *Dispatcher) DispatchTo(ctx context.Context, target *store.Incubator, subtaskID string, taskDef map[string]any) (string, error) {
	if target.ListenPort <= 0 || target.LastIP == "" {
		return "", errors.New("target incubator unreachable")
	}
	dest := &net.UDPAddr{IP: net.ParseIP(target.LastIP), Port: target.ListenPort}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		msgID := udp.NewID()
		payload := map[string]any{"subtask_id": subtaskID, "task_def": taskDef, "attempt": attempt}
		env, err := udp.New(udp.MsgDispatch, payload)
		if err != nil {
			return "", err
		}
		env.MsgID = msgID
		b, _ := json.Marshal(env)
		if err := d.store.InsertDispatchLog(udp.NewID(), subtaskID, target.ID, msgID); err != nil {
			return "", err
		}
		// 先登记 pending 再发包：localhost/低延迟下 ACK 可能早于登记到达（否则必然假超时）
		ch := make(chan error, 1)
		d.pending.Store(msgID, ch)
		if _, err := d.conn.WriteToUDP(b, dest); err != nil {
			d.pending.Delete(msgID)
			return "", err
		}
		if err := d.waitAck(ctx, msgID, ch); err == nil {
			return target.ID, nil
		} else {
			lastErr = err
			d.store.SetDispatchAck(msgID, "timeout")
			time.Sleep(time.Duration(1<<uint(attempt-1)) * time.Second) // 指数退避 1/2s
		}
	}
	return "", fmt.Errorf("dispatch timeout after %d attempts: %w", maxAttempts, lastErr)
}

// waitAck 等待已登记通道的 ACK（登记由调用方在发包前完成，避免竞态）
func (d *Dispatcher) waitAck(ctx context.Context, msgID string, ch chan error) error {
	defer d.pending.Delete(msgID)
	select {
	case err := <-ch:
		return err
	case <-time.After(ackTimeout):
		return errors.New("ack timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}
