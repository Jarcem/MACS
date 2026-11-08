// Package gateway 模块 #2：UDP 接入网关
// 256KB 上限（附录 B.3）+ msg_id 幂等去重（10min 窗口）+ 按 type 路由 + 统一回 ACK
package gateway

import (
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"macs.local/tower/internal/udp"
)

// Handler 处理一个上行消息；返回的 bytes 作为 ACK payload（nil 且 err==nil 时回 {"ok":true}）
type Handler func(env udp.Envelope, src *net.UDPAddr) ([]byte, error)

type Gateway struct {
	conn     *net.UDPConn
	handlers map[string]Handler
	seen     sync.Map // msg_id → 过期时刻
}

func New(conn *net.UDPConn) *Gateway {
	g := &Gateway{conn: conn, handlers: map[string]Handler{}}
	go g.sweep()
	return g
}

func (g *Gateway) On(msgType string, h Handler) { g.handlers[msgType] = h }

func (g *Gateway) Conn() *net.UDPConn { return g.conn }

func (g *Gateway) Serve() {
	buf := make([]byte, 256*1024)
	for {
		n, src, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		var env udp.Envelope
		if json.Unmarshal(buf[:n], &env) != nil || env.MsgID == "" || env.Type == "" {
			continue // 非协议包静默丢弃
		}
		if _, dup := g.seen.LoadOrStore(env.MsgID, time.Now().Add(10*time.Minute).UnixNano()); dup {
			log.Printf(`{"level":"warn","module":"gateway","msg":"duplicate msg_id=%s type=%s dropped"}`, env.MsgID, env.Type)
			continue // 幂等去重（6.4.2：重放不重复执行）
		}
		h, ok := g.handlers[env.Type]
		var payload []byte
		if ok {
			if p, err := h(env, src); err != nil {
				log.Printf(`{"level":"warn","module":"gateway","msg":"%s handler: %v"}`, env.Type, err)
				payload, _ = json.Marshal(map[string]any{"ok": false, "error": err.Error()})
			} else if p != nil {
				payload = p
			}
		}
		if payload == nil {
			payload = []byte(`{"ok":true}`)
		}
		if env.Type == udp.MsgAck {
			continue // ACK 不再回 ACK，避免 ACK 乒乓风暴
		}
		ack, _ := json.Marshal(udp.Envelope{
			MsgID: udp.NewID(), Type: udp.MsgAck, Version: udp.Version35,
			InReplyTo: env.MsgID, Payload: payload,
		})
		_, _ = g.conn.WriteToUDP(ack, src)
	}
}

func (g *Gateway) sweep() {
	for range time.Tick(2 * time.Minute) {
		now := time.Now().UnixNano()
		g.seen.Range(func(k, v any) bool {
			if v.(int64) < now {
				g.seen.Delete(k)
			}
			return true
		})
	}
}
