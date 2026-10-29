package server

import (
	"time"

	"github.com/gorilla/websocket"
)

// pingInterval sends traffic through idle proxies. Three missed intervals
// bound detection of a broken connection without renewing authentication.
var pingInterval = 25 * time.Second

// keepAlive pings conn until stop is closed or a ping fails. WriteControl is
// safe to call concurrently with the connection's other write methods.
func keepAlive(conn *websocket.Conn, stop <-chan struct{}) {
	interval := pingInterval
	pongWait := 3 * interval
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
}
