package router

import (
	"cp-remote-access-api/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

// generateTestToken: 테스트용 JWT를 생성하는 헬퍼 함수 (HS512 고정)
func generateTestToken(secret []byte, claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	return token.SignedString(secret)
}

// withBearerToken: Bearer 토큰을 생성하여 Authorization 헤더에 추가하는 setup 함수 반환
func withBearerToken(claims jwt.Claims, secret []byte) func(req *http.Request) {
	return func(req *http.Request) {
		token, _ := generateTestToken(secret, claims)
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// withWebSocketToken: 토큰을 생성하여 Sec-WebSocket-Protocol 헤더에 추가하는 setup 함수 반환
func withWebSocketToken(claims jwt.Claims, secret []byte) func(req *http.Request) {
	return func(req *http.Request) {
		token, _ := generateTestToken(secret, claims)
		req.Header.Set("Sec-WebSocket-Protocol", "bearer, "+token)
	}
}

// CustomClaims: 'Invalid Claims Type' 테스트를 위한 커스텀 클레임 구조체
type CustomClaims struct {
	Foo string `json:"foo"`
	jwt.RegisteredClaims
}

// AuthMiddleware의 JWT 인증 로직 검증
// 토큰의 유효성, 만료, 서명, 누락 등 다양한 시나리오 확인
func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jwtSecret := []byte(config.Env.JwtSecret)
	wrongSecret := []byte("a-different-secret-key-that-is-very-long-and-secure")

	// 반복적으로 사용되는 claims 미리 정의
	validClaims := jwt.MapClaims{
		"userAuthId": "test-user",
		"exp":        float64(time.Now().Add(time.Hour).Unix()),
	}
	expiredClaims := jwt.MapClaims{
		"userAuthId": "test-user",
		"exp":        float64(time.Now().Add(-time.Hour).Unix()),
	}
	missingUserClaim := jwt.MapClaims{
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}

	testCases := []struct {
		name               string
		setupRequest       func(req *http.Request)
		expectedStatusCode int
		expectedBody       string
	}{
		{
			// 성공: 정상적인 Bearer 토큰
			name:               "Success - Valid Bearer Token",
			setupRequest:       withBearerToken(validClaims, jwtSecret),
			expectedStatusCode: http.StatusOK,
			expectedBody:       `{"message":"passed"}`,
		},
		{
			// 성공: 웹소켓 프로토콜을 통한 정상 토큰
			name:               "Success - Valid WebSocket Protocol Token",
			setupRequest:       withWebSocketToken(validClaims, jwtSecret),
			expectedStatusCode: http.StatusOK,
			expectedBody:       `{"message":"passed"}`,
		},
		{
			// 실패: 토큰이 없는 경우
			name:               "Failure - No Token Provided",
			setupRequest:       func(req *http.Request) {},
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"MISSING_JWT"`,
		},
		{
			// 실패: 만료된 토큰
			name:               "Failure - Expired Token",
			setupRequest:       withBearerToken(expiredClaims, jwtSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_EXPIRED"`,
		},
		{
			// 실패: 잘못된 키로 서명된 토큰
			name:               "Failure - Token Signed with Wrong Secret",
			setupRequest:       withBearerToken(validClaims, wrongSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_FAILED"`,
		},
		{
			// 실패: 필수 클레임(userAuthId)이 누락된 토큰
			name:               "Failure - Missing userAuthId claim",
			setupRequest:       withBearerToken(missingUserClaim, jwtSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"ApiAccessDenied"`,
		},
		{
			// 실패: 토큰 서명 알고리즘이 다른 경우 (HS512가 아님)
			name: "Failure - Token with Wrong Signing Method",
			// 이 케이스는 generateTestToken 헬퍼를 사용할 수 없으므로 기존 로직 유지
			setupRequest: func(req *http.Request) {
				claims := jwt.MapClaims{"userAuthId": "test-user"}
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString(jwtSecret)
				req.Header.Set("Authorization", "Bearer "+tokenString)
			},
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_FAILED"`,
		},
		{
			// 실패: 토큰의 Claims 구조체가 예상과 다른 경우
			name: "Failure - Invalid Claims Type",
			setupRequest: func(req *http.Request) {
				claims := CustomClaims{
					"test",
					jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
				}
				token, _ := generateTestToken(jwtSecret, claims)
				req.Header.Set("Authorization", "Bearer "+token)
			},
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"ApiAccessDenied"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(AuthMiddleware())
			r.GET("/test", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"message": "passed"})
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/test", nil)

			tc.setupRequest(req)

			r.ServeHTTP(w, req)

			assert.Equal(t, tc.expectedStatusCode, w.Code)
			assert.Contains(t, w.Body.String(), tc.expectedBody)
		})
	}
}
