package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 라우터 설정(SetupRouter)의 무결성을 검증하는 단위 테스트
// 서버 실행 없이 라우팅 테이블과 기본 핸들러의 동작을 확인
func TestSetupRouter(t *testing.T) {
	r := SetupRouter()

	// 라우팅 테이블에 명세된 모든 경로가 올바르게 등록되었는지 검증
	t.Run("Verify Route Registration", func(t *testing.T) {
		routes := r.Routes()
		expectedRoutes := map[string]string{
			"/livez":       http.MethodGet,
			"/readyz":      http.MethodGet,
			"/ws/exec":     http.MethodGet,
			"/shell/check": http.MethodGet,
		}

		assert.Len(t, routes, len(expectedRoutes), "Number of registered routes should match expected")

		for _, route := range routes {
			expectedMethod, ok := expectedRoutes[route.Path]
			assert.True(t, ok, "Route %s is not expected", route.Path)
			assert.Equal(t, expectedMethod, route.Method, "Method for route %s is incorrect", route.Path)
		}
	})

	// 인증이 필요 없는 기본 엔드포인트(/livez, /readyz)의 실제 동작을 검증
	t.Run("Verify Basic Endpoints", func(t *testing.T) {
		testCases := []struct {
			path               string
			expectedStatusCode int
			expectedBody       string
		}{
			{path: "/livez", expectedStatusCode: http.StatusOK, expectedBody: "livez"},
			{path: "/readyz", expectedStatusCode: http.StatusOK, expectedBody: "readyz"},
		}

		for _, tc := range testCases {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.expectedStatusCode, w.Code, "Status code for path %s should match", tc.path)
			assert.Equal(t, tc.expectedBody, w.Body.String(), "Body for path %s should match", tc.path)
		}
	})
}
