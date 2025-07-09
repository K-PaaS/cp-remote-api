package controller

import (
	"github.com/gorilla/websocket"
	"io"
	"net/http"
)

var Upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS 허용
}

type webSocketStream struct {
	conn   *websocket.Conn
	readCh chan []byte
}

func newWebSocketStream(conn *websocket.Conn) *webSocketStream {
	s := &webSocketStream{
		conn:   conn,
		readCh: make(chan []byte),
	}

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(s.readCh)
				return
			}
			s.readCh <- msg
		}
	}()

	return s
}

func (s *webSocketStream) Read(p []byte) (int, error) {
	msg, ok := <-s.readCh
	if !ok {
		return 0, io.EOF
	}
	return copy(p, msg), nil
}

func (s *webSocketStream) Write(p []byte) (int, error) {
	return len(p), s.conn.WriteMessage(websocket.TextMessage, p)
}
