package controller

import (
	"bytes"
	"context"
	"cp-remote-access-api/internal/vault"
	"cp-remote-access-api/model"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

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
	"k8s.io/client-go/rest"
	fakerest "k8s.io/client-go/rest/fake"
	"k8s.io/client-go/tools/remotecommand"

	fakecorev1 "k8s.io/client-go/kubernetes/typed/core/v1/fake"
)

// GetClusterInfo 함수의 모든 시나리오 검증
func TestGetClusterInfo(t *testing.T) {
	monkey.Patch(vault.ConfigFromEnv, func() *vault.Config {
		return &vault.Config{}
	})
	defer monkey.UnpatchAll()

	var vc *vault.Client

	testCases := []struct {
		name           string
		setupMocks     func()
		expectedResult model.ClusterCredential
		expectFatal    bool
		expectedMsg    string
	}{
		{
			// 성공: 정상적으로 클러스터 정보를 가져오는 경우
			name: "Success - Fetches cluster info",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) { return "https://mock-api.server:6443", nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _ string) (string, error) { return "mock-bearer-token", nil })
			},
			expectedResult: model.ClusterCredential{
				ClusterID:    "test-cluster",
				APIServerURL: "https://mock-api.server:6443",
				BearerToken:  "mock-bearer-token",
			},
			expectFatal: false,
		},
		{
			// 실패: 클러스터 토큰을 가져오는 데 실패하는 경우
			name: "Failure - Fails to get cluster token",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) { return "https://mock-api.server:6443", nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _ string) (string, error) { return "", errors.New("failed to get token") })
			},
			expectFatal: true,
			expectedMsg: "클러스터 정보 조회 실패: failed to get token",
		},
		{
			// 실패: Vault 클라이언트 생성에 실패하는 경우
			name: "Failure - Vault client creation fails",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return nil, errors.New("vault client creation failed") })
			},
			expectFatal: true,
			expectedMsg: "Vault 클라이언트 생성 실패: vault client creation failed",
		},
		{
			// 성공(버그 동작 확인): GetClusterAPI가 실패해도 에러가 덮어쓰여 무시되는 경우
			name: "Bug Check - Error is ignored when GetClusterAPI fails",
			setupMocks: func() {
				monkey.Patch(vault.NewClient, func(cfg *vault.Config) (*vault.Client, error) { return &vault.Client{}, nil })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterAPI", func(_ *vault.Client, _ string) (string, error) { return "", errors.New("failed to get api") })
				monkey.PatchInstanceMethod(reflect.TypeOf(vc), "GetClusterToken", func(_ *vault.Client, _, _, _ string) (string, error) { return "mock-token-anyway", nil })
			},
			expectedResult: model.ClusterCredential{
				ClusterID:    "test-cluster",
				APIServerURL: "",
				BearerToken:  "mock-token-anyway",
			},
			expectFatal: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(monkey.UnpatchAll)
			tc.setupMocks()

			if tc.expectFatal {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("Expected a panic from log.Fatalf, but got none")
					}
				}()
				monkey.Patch(log.Fatalf, func(format string, v ...interface{}) {
					actualMsg := fmt.Sprintf(format, v...)
					if actualMsg != tc.expectedMsg {
						t.Errorf("Incorrect fatal log message.\nExpected: %s\nActual:   %s", tc.expectedMsg, actualMsg)
					}
					panic("log.Fatalf called")
				})
				GetClusterInfo("test-cluster", "test-user", "ADMIN")
			} else {
				result, err := GetClusterInfo("test-cluster", "test-user", "ADMIN")
				if err != nil {
					t.Errorf("Expected no error, but got: %v", err)
				}
				if !reflect.DeepEqual(result, tc.expectedResult) {
					t.Errorf("Result does not match expected.\nExpected: %+v\nActual:   %+v", tc.expectedResult, result)
				}
			}
		})
	}
}

// fakeK8sClientFactory: 성공 경로 테스트용 가짜 clientset 팩토리.
type fakeK8sClientFactory struct {
	clientset kubernetes.Interface
}

func (f *fakeK8sClientFactory) NewForConfig(c *rest.Config) (kubernetes.Interface, error) {
	return f.clientset, nil
}

// fakeFailingK8sClientFactory: 클라이언트 생성 실패 테스트용 팩토리.
type fakeFailingK8sClientFactory struct{}

func (f *fakeFailingK8sClientFactory) NewForConfig(c *rest.Config) (kubernetes.Interface, error) {
	return nil, errors.New("simulated clientset creation failure")
}

// FakeExecutor: 원격 명령어 실행의 성공/실패 시뮬레이션용.
type FakeExecutor struct {
	shouldSucceed bool
}

func (e *FakeExecutor) Stream(options remotecommand.StreamOptions) error {
	if e.shouldSucceed {
		return nil
	}
	return errors.New("this container does not have a shell")
}
func (e *FakeExecutor) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	return e.Stream(options)
}

// CheckShellHandler의 모든 시나리오 검증
func TestCheckShellHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	testCases := []struct {
		name               string
		setup              func(t *testing.T)
		claims             any
		requestURL         string
		expectedStatusCode int
		assertion          func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			// 성공: 쉘이 있는 컨테이너와 없는 컨테이너가 혼재된 경우
			name:   "Success - Mixed shell statuses",
			claims: jwt.MapClaims{"userAuthId": "test-user-id", "userType": "SUPER_ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				mockPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-ns"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "container-with-shell"}, {Name: "container-without-shell"}}},
				}
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset(mockPod)}
				monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })
				callCount := 0
				monkey.Patch(remotecommand.NewSPDYExecutor, func(c *rest.Config, m string, u *url.URL) (remotecommand.Executor, error) {
					callCount++
					return &FakeExecutor{shouldSucceed: callCount == 1}, nil
				})
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
			},
			requestURL:         "/shell/check?namespace=test-ns&pod=test-pod&clusterId=test-cluster",
			expectedStatusCode: http.StatusOK,
			assertion: func(t *testing.T, w *httptest.ResponseRecorder) {
				var statuses []ContainerShellStatus
				err := json.Unmarshal(w.Body.Bytes(), &statuses)
				require.NoError(t, err, "Response body should unmarshal correctly")
				require.Len(t, statuses, 2, "Expected 2 container statuses")
				assert.True(t, statuses[0].HasShell, "Expected 'container-with-shell' to have a shell")
				assert.False(t, statuses[1].HasShell, "Expected 'container-without-shell' not to have a shell")
			},
		},
		{
			// 실패: 컨텍스트에 claims가 없는 경우
			name:               "Failure - No claims in context",
			claims:             nil,
			requestURL:         "/shell/check?namespace=test-ns&pod=test-pod&clusterId=test-cluster",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			// 실패: claims의 타입이 유효하지 않은 경우
			name:               "Failure - Invalid claims format",
			claims:             "this is not a valid map claims object",
			requestURL:         "/shell/check",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			// 실패: GetClusterInfo API 호출이 실패하는 경우
			name:   "Failure - GetClusterInfo fails",
			claims: jwt.MapClaims{"userAuthId": "test-user-id", "userType": "SUPER_ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{}, errors.New("simulated vault error")
				})
				t.Cleanup(monkey.UnpatchAll)
			},
			requestURL:         "/shell/check",
			expectedStatusCode: http.StatusInternalServerError,
		},
		{
			// 실패: K8s clientset 생성에 실패하는 경우
			name:   "Failure - Clientset creation fails",
			claims: jwt.MapClaims{"userAuthId": "test-user-id", "userType": "SUPER_ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeFailingK8sClientFactory{}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
			},
			requestURL:         "/shell/check",
			expectedStatusCode: http.StatusInternalServerError,
		},
		{
			// 실패: 요청한 Pod가 존재하지 않는 경우
			name:   "Failure - Pod not found",
			claims: jwt.MapClaims{"userAuthId": "test-user-id", "userType": "SUPER_ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
			},
			requestURL:         "/shell/check?namespace=test-ns&pod=non-existent-pod&clusterId=test-cluster",
			expectedStatusCode: http.StatusNotFound,
			assertion: func(t *testing.T, w *httptest.ResponseRecorder) {
				var response map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &response)
				require.NoError(t, err, "Response should be valid JSON")
				expectedError := "Pod not found: pods \"non-existent-pod\" not found"
				assert.Equal(t, expectedError, response["error"], "Error message should match")
			},
		},
		{
			// 실패: 원격 명령 실행기(Executor) 생성에 실패하는 경우
			name:   "Failure - Executor creation fails",
			claims: jwt.MapClaims{"userAuthId": "test-user-id", "userType": "SUPER_ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				mockPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-ns"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test-container"}}},
				}
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset(mockPod)}
				monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })
				monkey.Patch(remotecommand.NewSPDYExecutor, func(c *rest.Config, m string, u *url.URL) (remotecommand.Executor, error) {
					return nil, errors.New("simulated executor creation failure")
				})
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
			},
			requestURL:         "/shell/check?pod=test-pod&namespace=test-ns",
			expectedStatusCode: http.StatusOK,
			assertion: func(t *testing.T, w *httptest.ResponseRecorder) {
				var statuses []ContainerShellStatus
				err := json.Unmarshal(w.Body.Bytes(), &statuses)
				require.NoError(t, err, "Response should unmarshal")
				require.Len(t, statuses, 1, "Should be one status")
				assert.False(t, statuses[0].HasShell, "HasShell should be false on executor failure")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodGet, tc.requestURL, nil)
			if tc.claims != nil {
				c.Set("claims", tc.claims)
			}
			if tc.setup != nil {
				tc.setup(t)
			}
			CheckShellHandler(c)
			if w.Code != tc.expectedStatusCode {
				t.Errorf("Expected status code %d, but got %d", tc.expectedStatusCode, w.Code)
			}
			if tc.assertion != nil {
				tc.assertion(t, w)
			}
		})
	}
}

// ExecWebSocketHandler의 모든 시나리오 검증
func TestExecWebSocketHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	testCases := []struct {
		name               string
		setup              func(t *testing.T)
		claims             any
		isWebSocketTest    bool
		expectedStatusCode int
		expectedBody       string
	}{
		{
			// 실패: 인증 정보(claims)가 없는 경우
			name:               "Failure - No claims in context",
			isWebSocketTest:    false,
			claims:             nil,
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "claims not found",
		},
		{
			// 실패: 클러스터 정보 조회에 실패하는 경우
			name:            "Failure - GetClusterInfo fails",
			isWebSocketTest: false,
			claims:          jwt.MapClaims{"userAuthId": "test-user", "userType": "ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{}, errors.New("simulated vault error")
				})
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "failed to get cluster info",
		},
		{
			// 실패: K8s clientset 생성에 실패하는 경우
			name:            "Failure - Clientset creation fails",
			isWebSocketTest: true,
			claims:          jwt.MapClaims{"userAuthId": "test-user", "userType": "ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeFailingK8sClientFactory{}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
			},
			expectedStatusCode: http.StatusSwitchingProtocols,
			expectedBody:       "Failed to create clientset",
		},
		{
			// 실패: Stream 실행에 실패하는 경우
			name:            "Failure - Stream fails",
			isWebSocketTest: true,
			claims:          jwt.MapClaims{"userAuthId": "test-user", "userType": "ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "http://127.0.0.1:9999", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
				monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface {
					return &fakerest.RESTClient{
						Resp: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(""))},
					}
				})
			},
			expectedStatusCode: http.StatusSwitchingProtocols,
			expectedBody:       "Exec stream error",
		},
		{
			// 실패: Executor 생성에 실패하는 경우
			name:            "Failure - Executor creation fails",
			isWebSocketTest: true,
			claims:          jwt.MapClaims{"userAuthId": "test-user", "userType": "ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
				monkey.Patch(newExecutor, func(cs kubernetes.Interface, c *rest.Config, p, n, co string) (remotecommand.Executor, error) {
					return nil, errors.New("simulated executor creation error")
				})
			},
			expectedStatusCode: http.StatusSwitchingProtocols,
			expectedBody:       "Executor error",
		},
		{
			// 성공: 정상적으로 WebSocket 연결이 수립되는 경우
			name:            "Success - WebSocket connection",
			isWebSocketTest: true,
			claims:          jwt.MapClaims{"userAuthId": "test-user", "userType": "ADMIN"},
			setup: func(t *testing.T) {
				monkey.Patch(GetClusterInfo, func(c, u, ut string) (model.ClusterCredential, error) {
					return model.ClusterCredential{APIServerURL: "https://mock-k8s.local", BearerToken: "mock-token"}, nil
				})
				K8sClientFactoryImpl = &fakeK8sClientFactory{clientset: fake.NewSimpleClientset()}
				t.Cleanup(func() { K8sClientFactoryImpl = &realK8sClientFactory{} })
				monkey.Patch((*fakecorev1.FakeCoreV1).RESTClient, func(*fakecorev1.FakeCoreV1) rest.Interface { return &fakerest.RESTClient{} })
				monkey.Patch(remotecommand.NewWebSocketExecutor, func(c *rest.Config, m, u string) (remotecommand.Executor, error) {
					return &FakeExecutor{shouldSucceed: true}, nil
				})
			},
			expectedStatusCode: http.StatusSwitchingProtocols,
			expectedBody:       "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(monkey.UnpatchAll)
			if tc.setup != nil {
				tc.setup(t)
			}
			router := gin.New()
			router.GET("/ws/exec", func(c *gin.Context) {
				if tc.claims != nil {
					c.Set("claims", tc.claims)
				}
				ExecWebSocketHandler(c)
			})
			server := httptest.NewServer(router)
			defer server.Close()

			if !tc.isWebSocketTest {
				resp, err := http.Get(server.URL + "/ws/exec")
				require.NoError(t, err, "HTTP GET should not fail")
				defer resp.Body.Close()
				assert.Equal(t, tc.expectedStatusCode, resp.StatusCode, "HTTP status code should match")
				bodyBytes, _ := io.ReadAll(resp.Body)
				assert.Contains(t, string(bodyBytes), tc.expectedBody, "Response body should contain expected text")
			} else {
				wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/exec"
				conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Sec-WebSocket-Protocol": []string{"bearer"}})
				require.NoError(t, err, "WebSocket dial should not fail")
				require.NotNil(t, resp, "HTTP response from WebSocket dial should not be nil")
				defer conn.Close()
				assert.Equal(t, tc.expectedStatusCode, resp.StatusCode, "HTTP status code for WebSocket upgrade should match")

				if tc.expectedBody != "" {
					_, msg, err := conn.ReadMessage()
					require.NoError(t, err, "Reading message from WebSocket should not fail")
					assert.Contains(t, string(msg), tc.expectedBody, "WebSocket message body should contain expected text")
				}
			}
		})
	}
}
