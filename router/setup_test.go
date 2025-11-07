package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSetupRouter: /actuator/health 엔드포인트 그룹 테스트
// 인증이 필요 없는 헬스 체크 응답 확인
func TestSetupRouter_HealthCheckEndpoints(t *testing.T) {
	// Arrange: 테스트 라우터 설정
	r := SetupRouter()

	testCases := []struct {
		path               string // 테스트 경로
		expectedStatusCode int    // 기대 상태 코드
		expectedBody       string // 기대 응답 본문
	}{
		{
			path:               "/actuator/health",
			expectedStatusCode: http.StatusOK,
			expectedBody:       "OK",
		},
		{
			path:               "/actuator/health/liveness",
			expectedStatusCode: http.StatusOK,
			expectedBody:       "livez",
		},
		{
			path:               "/actuator/health/readiness",
			expectedStatusCode: http.StatusOK,
			expectedBody:       "readyz",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			// Arrange: 요청 및 응답 레코더 준비
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tc.path, nil)

			// Act: HTTP 요청 수행
			r.ServeHTTP(w, req)

			// Assert: 결과 검증
			assert.Equal(t, tc.expectedStatusCode, w.Code)
			assert.Equal(t, tc.expectedBody, w.Body.String())
		})
	}
}
