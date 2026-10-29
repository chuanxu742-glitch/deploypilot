package server

import (
	"time"

	"github.com/gorilla/websocket"
)

// pingInterval is how often idle WebSockets are pinged. Cloudflare closes
// tunnelled WebSockets that carry no traffic for about 100 seconds, which
// showed up as "连接已关闭" in the terminal after a short idle period.
var pingInterval = 25 * time.Second

// keepAlive pings conn until stop is closed or a ping fails. WriteControl is
// safe to call concurrently with the connection's other write methods.
func keepAlive(conn *websocket.Conn, stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()
}
