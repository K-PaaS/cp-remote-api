package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webSocketStream의 정상적인 읽기/쓰기 상호작용을 검증
func TestWebSocketStream_ReadWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		require.NoError(t, err, "Upgrading to websocket should not fail")
		defer conn.Close()

		stream := newWebSocketStream(conn)

		buffer := make([]byte, 1024)
		n, err := stream.Read(buffer)
		require.NoError(t, err, "Reading from stream should not fail")
		assert.Equal(t, "hello from client", string(buffer[:n]), "Server should read the correct message from client")

		_, err = stream.Write([]byte("hello from server"))
		require.NoError(t, err, "Writing to stream should not fail")
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Dialing websocket server should not fail")
	defer clientConn.Close()

	err = clientConn.WriteMessage(websocket.TextMessage, []byte("hello from client"))
	require.NoError(t, err, "Client writing message should not fail")

	_, p, err := clientConn.ReadMessage()
	require.NoError(t, err, "Client reading message should not fail")
	assert.Equal(t, "hello from server", string(p), "Client should receive the correct message from server")
}

// webSocketStream에서 클라이언트 연결 종료 시 Read 메서드가 io.EOF를 반환하는지 검증
func TestWebSocketStream_ConnectionClosed(t *testing.T) {
	done := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		require.NoError(t, err, "Upgrading to websocket should not fail")
		defer conn.Close()

		stream := newWebSocketStream(conn)

		time.Sleep(50 * time.Millisecond)
		_, err = stream.Read(make([]byte, 1024))
		assert.Equal(t, io.EOF, err, "Reading from a closed stream should return io.EOF")

		close(done)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Dialing websocket server should not fail")
	clientConn.Close()

	select {
	case <-done:
		// Test passed
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out: server side assertion was not completed.")
	}
}
