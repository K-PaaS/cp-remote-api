package controller

import (
	"context"
	"cp-remote-access-api/config"
	"cp-remote-access-api/internal/vault"
	"cp-remote-access-api/model"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouk/monkey"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	fakecorev1 "k8s.io/client-go/kubernetes/typed/core/v1/fake"
	"k8s.io/client-go/rest"
	fakerest "k8s.io/client-go/rest/fake"
	"k8s.io/client-go/tools/remotecommand"
)

// --- [ 1. 헬퍼 Structs & Funcs (K8s) ] ---

type fakeK8sClientFactory struct {
	clientset kubernetes.Interface
}

func (f *fakeK8sClientFactory) NewForConfig(c *rest.Config) (kubernetes.Interface, error) {
	return f.clientset, nil
}

type fakeFailingK8sClientFactory struct{}

func (f *fakeFailingK8sClientFactory) NewForConfig(c *rest.Config) (kubernetes.Interface, error) {
	return nil, errors.New("simulated clientset creation failure")
}

type FakeExecutor struct {
	StreamFunc func(options remotecommand.StreamOptions) error
}

func (e *FakeExecutor) Stream(options remotecommand.StreamOptions) error {
	if e.StreamFunc != nil {
		return e.StreamFunc(options)
	}
	return nil
}

func (e *FakeExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	return e.Stream(options)
}

// --- [ 2. GetClusterInfo 테스트 ] ---

func TestGetClusterInfo(t *testing.T) {
	config.Env = &config.EnvConfigs{}
	monkey.Patch(vault.ConfigFromEnv, func() *vault.Config {
		return &vault.Config{}
	})
	defer monkey.UnpatchAll()
	var vc *vault.Client

	testCases := []struct {
		name           string
		setupMocks     func()
		expectedResult model.ClusterCredential
		expectedError  string
	}{
		{
			name: "Success - Fetches cluster info",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) {
					return "https://mock-api.server:6443", nil
				})
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _, _ string) (string, error) {
					return "mock-bearer-token", nil
				})
			},
			expectedResult: model.ClusterCredential{
				ClusterID:    "test-cluster",
				APIServerURL: "https://mock-api.server:6443",
				BearerToken:  "mock-bearer-token",
			},
			expectedError: "",
		},
		{
			name: "Failure - Fails to get cluster token",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) {
					return "https://mock-api.server:6443", nil
				})
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _, _ string) (string, error) {
					return "", errors.New("failed to get token")
				})
			},
			expectedError: "failed to get token",
		},
		{
			name: "Failure - Vault client creation fails",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) {
					return nil, errors.New("vault client creation failed")
				})
			},
			expectedError: "vault client creation failed",
		},
		{
			name: "Failure - GetClusterAPI fails",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) {
					return "", errors.New("failed to get api")
				})
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _, _ string) (string, error) {
					assert.Fail(t, "GetClusterToken should not be called if GetClusterAPI fails") // t.Fatal 대신
					return "", nil
				})
			},
			expectedError: "failed to get api",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(monkey.UnpatchAll)
			tc.setupMocks()
			monkey.Patch(log.Printf, func(format string, v ...interface{}) {})

			result, err := GetClusterInfo("test-cluster", "test-user", "ADMIN", "default")

			if tc.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedResult, result)
			}
		})
	}
}

// --- [ 3. CheckShellHandler 테스트 (REST) ] ---

func TestCheckShellHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config.Env = &config.EnvConfigs{}

	mockPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "shell-ok-container"},
			{Name: "shell-fail-container"},
		}},
	}

	testCases := []struct {
		name                 string
		setupRequest         func(c *gin.Context)
		setupMocks           func(t *testing.T, clientset *fake.Clientset)
		expectedStatus       int
		expectedBodyContains string
	}{
		{
			name: "Success - Mixed shell status",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check?pod=test-pod&namespace=test-ns&clusterId=c1")
				c.Set("claims", jwt.MapClaims{"userAuthId": "u1", "userType": "USER"})
			},
			setupMocks: func(t *testing.T, clientset *fake.Clientset) {
				monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
					return model.ClusterCredential{BearerToken: "token"}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: clientset}
				clientset.CoreV1().Pods("test-ns").Create(context.TODO(), mockPod, metav1.CreateOptions{})

				monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface {
					return &fakerest.RESTClient{}
				})

				callCount := 0
				monkey.Patch(remotecommand.NewSPDYExecutor, func(c *rest.Config, m string, u *url.URL) (remotecommand.Executor, error) {
					callCount++
					streamFunc := func(options remotecommand.StreamOptions) error {
						if callCount == 1 {
							return nil // shell-ok
						}
						return errors.New("no shell")
					}
					return &FakeExecutor{StreamFunc: streamFunc}, nil
				})
			},
			expectedStatus:       http.StatusOK,
			expectedBodyContains: `[{"name":"shell-ok-container","hasShell":true},{"name":"shell-fail-container","hasShell":false}]`,
		},
		{
			name: "Failure - No Claims",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check")
			},
			setupMocks:           func(t *testing.T, clientset *fake.Clientset) {},
			expectedStatus:       http.StatusUnauthorized,
			expectedBodyContains: "Claims not found",
		},
		{
			name: "Failure - Invalid Claims",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check")
				c.Set("claims", "not a map")
			},
			setupMocks:           func(t *testing.T, clientset *fake.Clientset) {},
			expectedStatus:       http.StatusUnauthorized,
			expectedBodyContains: "Invalid claims format",
		},
		{
			name: "Failure - GetClusterInfo fails",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check?namespace=test-ns&clusterId=c1")
				c.Set("claims", jwt.MapClaims{"userAuthId": "u1", "userType": "USER"})
			},
			setupMocks: func(t *testing.T, clientset *fake.Clientset) {
				monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
					return model.ClusterCredential{}, errors.New("vault failed")
				})
			},
			expectedStatus:       http.StatusInternalServerError,
			expectedBodyContains: "Failed to get cluster info",
		},
		{
			name: "Failure - Clientset fails",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check")
				c.Set("claims", jwt.MapClaims{"userAuthId": "u1", "userType": "USER"})
			},
			setupMocks: func(t *testing.T, clientset *fake.Clientset) {
				monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
					return model.ClusterCredential{}, nil
				})
				K8sClientFactoryImpl = &fakeFailingK8sClientFactory{}
			},
			expectedStatus:       http.StatusInternalServerError,
			expectedBodyContains: "Failed to create clientset",
		},
		{
			name: "Failure - Pod not found",
			setupRequest: func(c *gin.Context) {
				c.Request.URL, _ = url.Parse("/shell/check?pod=not-found-pod&namespace=test-ns&clusterId=c1")
				c.Set("claims", jwt.MapClaims{"userAuthId": "u1", "userType": "USER"})
			},
			setupMocks: func(t *testing.T, clientset *fake.Clientset) {
				monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
					return model.ClusterCredential{}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: clientset}
			},
			expectedStatus:       http.StatusNotFound,
			expectedBodyContains: "Pod not found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(monkey.UnpatchAll)
			K8sClientFactoryImpl = &realK8sClientFactory{}
			monkey.Patch(log.Printf, func(format string, v ...interface{}) {})

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = &http.Request{Header: make(http.Header)}
			clientset := fake.NewSimpleClientset()

			tc.setupRequest(c)
			tc.setupMocks(t, clientset)

			CheckShellHandler(c)

			assert.Equal(t, tc.expectedStatus, w.Code)
			if tc.expectedStatus == http.StatusOK {
				assert.JSONEq(t, tc.expectedBodyContains, w.Body.String())
			} else {
				assert.Contains(t, w.Body.String(), tc.expectedBodyContains)
			}
		})
	}
}

// --- [ 4. ExecWebSocketHandler 테스트 (WebSocket) ] ---

func setupTestServer(t *testing.T) *httptest.Server {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("claims", jwt.MapClaims{
			"userAuthId": "ws-user",
			"userType":   "USER",
			"exp":        float64(time.Now().Add(time.Hour).Unix()),
		})
		c.Next()
	})
	r.GET("/ws/exec", ExecWebSocketHandler)

	return httptest.NewServer(r)
}

func clientDial(t *testing.T, serverURL string, queryParams string) *websocket.Conn {
	wsURL := strings.Replace(serverURL, "http", "ws", 1) + "/ws/exec" + queryParams
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "WebSocket dial should not fail")
	return conn
}

func TestExecWebSocketHandler(t *testing.T) {
	config.Env = &config.EnvConfigs{}
	monkey.Patch(log.Printf, func(format string, v ...interface{}) {})

	t.Run("Success - WebSocket Stream", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		K8sClientFactoryImpl = &realK8sClientFactory{}
		server := setupTestServer(t)
		defer server.Close()

		monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
			return model.ClusterCredential{BearerToken: "token"}, nil
		})
		K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}

		monkey.Patch(newExecutor, func(cs kubernetes.Interface, cfg *rest.Config, p, n, co string) (remotecommand.Executor, error) {
			return &FakeExecutor{StreamFunc: func(options remotecommand.StreamOptions) error {
				options.Stdout.Write([]byte("hello from server"))
				time.Sleep(50 * time.Millisecond)
				return nil
			}}, nil
		})
		monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })

		clientConn := clientDial(t, server.URL, "?pod=p1&namespace=ns1&container=c1&clusterId=c1")
		defer clientConn.Close()
		_, msg, err := clientConn.ReadMessage()

		require.NoError(t, err)
		assert.Equal(t, "hello from server", string(msg))
	})

	t.Run("Failure - GetClusterInfo fails (before upgrade)", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ws/exec?namespace=ns1&clusterId=c1", nil)
		c.Set("claims", jwt.MapClaims{"userAuthId": "u1", "userType": "USER"})

		monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
			return model.ClusterCredential{}, errors.New("vault failed")
		})

		ExecWebSocketHandler(c)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "failed to get cluster info")
	})

	t.Run("Failure - K8sClientFactory fails", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		K8sClientFactoryImpl = &realK8sClientFactory{}
		server := setupTestServer(t)
		defer server.Close()

		monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
			return model.ClusterCredential{BearerToken: "token"}, nil
		})
		K8sClientFactoryImpl = &fakeFailingK8sClientFactory{}

		clientConn := clientDial(t, server.URL, "?clusterId=c1")
		defer clientConn.Close()
		_, msg, err := clientConn.ReadMessage()

		require.NoError(t, err)
		assert.Contains(t, string(msg), "Failed to create clientset")
	})

	t.Run("Failure - newExecutor fails", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		K8sClientFactoryImpl = &realK8sClientFactory{}
		server := setupTestServer(t)
		defer server.Close()

		monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
			return model.ClusterCredential{BearerToken: "token"}, nil
		})
		K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
		monkey.Patch(newExecutor, func(cs kubernetes.Interface, cfg *rest.Config, p, n, co string) (remotecommand.Executor, error) {
			return nil, errors.New("executor fail")
		})
		monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })

		clientConn := clientDial(t, server.URL, "?clusterId=c1")
		defer clientConn.Close()
		_, msg, err := clientConn.ReadMessage()

		require.NoError(t, err)
		assert.Contains(t, string(msg), "Executor error:executor fail")
	})

	t.Run("Failure - Stream fails", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		K8sClientFactoryImpl = &realK8sClientFactory{}
		server := setupTestServer(t)
		defer server.Close()

		monkey.Patch(GetClusterInfo, func(cID, uID, uType, ns string) (model.ClusterCredential, error) {
			return model.ClusterCredential{BearerToken: "token"}, nil
		})
		K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
		monkey.Patch(newExecutor, func(cs kubernetes.Interface, cfg *rest.Config, p, n, co string) (remotecommand.Executor, error) {
			return &FakeExecutor{StreamFunc: func(options remotecommand.StreamOptions) error {
				return errors.New("stream fail")
			}}, nil
		})
		monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })

		clientConn := clientDial(t, server.URL, "?clusterId=c1")
		defer clientConn.Close()
		_, msg, err := clientConn.ReadMessage()

		require.NoError(t, err)
		assert.Contains(t, string(msg), "Exec stream error: stream fail")
	})

	t.Run("Failure - No Claims (before upgrade)", func(t *testing.T) {
		t.Cleanup(monkey.UnpatchAll)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/ws/exec", nil)

		ExecWebSocketHandler(c)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "claims not found")
	})
}

// --- [ 5. webSocketStream 테스트 ] ---

func TestWebSocketStream_ReadWrite(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		conn, err := Upgrader.Upgrade(w, r, nil)
		require.NoError(t, err, "Server: Upgrade should not fail")
		defer conn.Close()

		stream := newWebSocketStream(conn)

		buffer := make([]byte, 1024)
		n, err := stream.Read(buffer)
		require.NoError(t, err, "Server: stream.Read should not fail")
		assert.Equal(t, "hello from client", string(buffer[:n]), "Server: Read incorrect message")

		_, err = stream.Write([]byte("hello from server"))
		require.NoError(t, err, "Server: stream.Write should not fail")
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Client: Dial should not fail")
	defer clientConn.Close()

	err = clientConn.WriteMessage(websocket.TextMessage, []byte("hello from client"))
	require.NoError(t, err, "Client: WriteMessage should not fail")

	_, p, err := clientConn.ReadMessage()
	require.NoError(t, err, "Client: ReadMessage should not fail")

	assert.Equal(t, "hello from server", string(p), "Client: Received incorrect message")
	wg.Wait()
}

func TestWebSocketStream_ConnectionClosed(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		conn, err := Upgrader.Upgrade(w, r, nil)
		require.NoError(t, err, "Server: Upgrade should not fail")
		defer conn.Close()

		stream := newWebSocketStream(conn)

		readDone := make(chan error)
		go func() {
			_, err := stream.Read(make([]byte, 1024))
			readDone <- err
		}()

		select {
		case err := <-readDone:
			assert.Equal(t, io.EOF, err, "Server: Read from closed stream should return io.EOF")
		case <-time.After(2 * time.Second):
			t.Error("Test timed out: server side assertion was not completed.")
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Client: Dial should not fail")

	clientConn.Close()
	wg.Wait()
}
