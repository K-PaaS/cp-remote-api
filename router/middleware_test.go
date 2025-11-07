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

// generateTestToken: 테스트용 JWT 생성 헬퍼 (HS512 고정)
func generateTestToken(secret []byte, claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	return token.SignedString(secret)
}

// withBearerToken: Authorization 헤더에 Bearer 토큰 주입
func withBearerToken(claims jwt.Claims, secret []byte) func(req *http.Request) {
	return func(req *http.Request) {
		token, _ := generateTestToken(secret, claims)
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// withWebSocketToken: Sec-WebSocket-Protocol 헤더에 토큰 주입
func withWebSocketToken(claims jwt.Claims, secret []byte) func(req *http.Request) {
	return func(req *http.Request) {
		token, _ := generateTestToken(secret, claims)
		req.Header.Set("Sec-WebSocket-Protocol", "bearer, "+token)
	}
}

// CustomClaims: 'Invalid Claims Type' 테스트용 커스텀 구조체
type CustomClaims struct {
	Foo string `json:"foo"`
	jwt.RegisteredClaims
}

// TestAuthMiddleware: JWT 인증 미들웨어 로직 검증
func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Arrange: 테스트용 config.Env 설정 (nil 패닉 방지)
	config.Env = &config.EnvConfigs{
		JwtSecret: "a-very-secure-test-secret-key-for-hs512-algo",
	}
	jwtSecret := []byte(config.Env.JwtSecret)
	wrongSecret := []byte("a-different-secret-key-that-is-very-long-and-secure")

	// Arrange: 테스트용 클레임 정의
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
	missingExpClaim := jwt.MapClaims{
		"userAuthId": "test-user",
	}

	testCases := []struct {
		name               string
		setupRequest       func(req *http.Request)
		expectedStatusCode int
		expectedBody       string
	}{
		{
			name:               "Success - Valid Bearer Token",
			setupRequest:       withBearerToken(validClaims, jwtSecret),
			expectedStatusCode: http.StatusOK,
			expectedBody:       `{"message":"passed"}`,
		},
		{
			name:               "Success - Valid WebSocket Protocol Token",
			setupRequest:       withWebSocketToken(validClaims, jwtSecret),
			expectedStatusCode: http.StatusOK,
			expectedBody:       `{"message":"passed"}`,
		},
		{
			name:               "Failure - No Token Provided",
			setupRequest:       func(req *http.Request) {},
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"MISSING_JWT"`,
		},
		{
			name:               "Failure - Expired Token",
			setupRequest:       withBearerToken(expiredClaims, jwtSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_EXPIRED"`,
		},
		{
			name:               "Failure - Token Signed with Wrong Secret",
			setupRequest:       withBearerToken(validClaims, wrongSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_FAILED"`,
		},
		{
			name:               "Failure - Missing userAuthId claim",
			setupRequest:       withBearerToken(missingUserClaim, jwtSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"ApiAccessDenied"`,
		},
		{
			name:               "Failure - Missing exp claim",
			setupRequest:       withBearerToken(missingExpClaim, jwtSecret),
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       `"TOKEN_FAILED"`,
		},
		{
			name: "Failure - Token with Wrong Signing Method",
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
			// Arrange: 라우터 및 더미 핸들러 설정
			r := gin.New()
			r.Use(AuthMiddleware())
			r.GET("/test", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"message": "passed"})
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/test", nil)

			// Arrange: 테스트 케이스별 헤더 설정
			tc.setupRequest(req)

			// Act: 요청 수행
			r.ServeHTTP(w, req)

			// Assert: 상태 코드 및 바디 검증
			assert.Equal(t, tc.expectedStatusCode, w.Code)
			assert.Contains(t, w.Body.String(), tc.expectedBody)
		})
	}
}
