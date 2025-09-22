package vault

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockVault: "성공" 시나리오를 위한 가짜 Vault 서버
func setupMockVault(t *testing.T) *httptest.Server {
	handler := http.NewServeMux()

	handler.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PUT", r.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"auth":{"client_token":"test-token","lease_duration":3600}}`)
	})

	handler.HandleFunc("/v1/secret/data/cluster/test-cluster", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"data":{"clusterApiUrl":"https://fake-cluster.api","clusterToken":"super-admin-token"}}}`)
	})

	handler.HandleFunc("/v1/secret/data/user/test-user/test-cluster", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"data":{"clusterToken":"user-specific-token"}}}`)
	})

	return httptest.NewServer(handler)
}

// setupFailingMockVault: "실패" 시나리오를 위한 가짜 Vault 서버
func setupFailingMockVault(t *testing.T) *httptest.Server {
	handler := http.NewServeMux()

	handler.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	})

	handler.HandleFunc("/v1/auth/approle/login-success", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"auth":{"client_token":"test-token"}}`)
	})
	handler.HandleFunc("/v1/secret/data/cluster/not-found-cluster", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	handler.HandleFunc("/v1/secret/data/cluster/malformed-cluster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})

	handler.HandleFunc("/v1/secret/data/user/any-user/malformed-cluster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})

	return httptest.NewServer(handler)
}

// Vault 클라이언트의 성공 시나리오 검증
func TestVaultClient_Success(t *testing.T) {
	mockServer := setupMockVault(t)
	defer mockServer.Close()

	t.Setenv("VAULT_URL", mockServer.URL)
	t.Setenv("VAULT_ROLE_ID", "test-role-id")
	t.Setenv("VAULT_SECRET_ID", "test-secret-id")

	cfg := ConfigFromEnv()
	client, err := NewClient(cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	// 성공: GetClusterInfo 정상 호출
	t.Run("GetClusterInfo successfully", func(t *testing.T) {
		info, err := client.GetClusterInfo("test-cluster")
		assert.NoError(t, err)
		assert.Equal(t, "test-cluster", info.ClusterID)
		assert.Equal(t, "https://fake-cluster.api", info.APIServerURL)
		assert.Equal(t, "super-admin-token", info.BearerToken)
	})

	// 성공: GetClusterAPI 정상 호출
	t.Run("GetClusterAPI successfully", func(t *testing.T) {
		apiURL, err := client.GetClusterAPI("test-cluster")
		assert.NoError(t, err)
		assert.Equal(t, "https://fake-cluster.api", apiURL)
	})

	// 성공: GetClusterToken (SUPER_ADMIN) 정상 호출
	t.Run("GetClusterToken for SUPER_ADMIN", func(t *testing.T) {
		token, err := client.GetClusterToken("test-cluster", "any-user", "SUPER_ADMIN")
		assert.NoError(t, err)
		assert.Equal(t, "super-admin-token", token)
	})

	// 성공: GetClusterToken (일반 사용자) 정상 호출
	t.Run("GetClusterToken for regular user", func(t *testing.T) {
		token, err := client.GetClusterToken("test-cluster", "test-user", "USER")
		assert.NoError(t, err)
		assert.Equal(t, "user-specific-token", token)
	})
}

// Vault 클라이언트의 실패 시나리오 검증
func TestVaultClient_Failures(t *testing.T) {
	// assertAllApiCallsFail: 클라이언트의 모든 API 호출이 실패하는지 검증하는 헬퍼.
	assertAllApiCallsFail := func(t *testing.T, client *Client, clusterID string) {
		t.Helper() // 이 함수가 테스트 헬퍼임을 명시

		_, err := client.GetClusterAPI(clusterID)
		require.Error(t, err, "GetClusterAPI should fail")

		_, err = client.GetClusterInfo(clusterID)
		require.Error(t, err, "GetClusterInfo should fail")

		_, err = client.GetClusterToken(clusterID, "any-user", "USER")
		require.Error(t, err, "GetClusterToken should fail")
	}

	mockServer := setupFailingMockVault(t)
	defer mockServer.Close()

	// 실패: NewClient 생성 시 URL 포맷이 잘못된 경우
	t.Run("NewClient fails on invalid URL", func(t *testing.T) {
		cfg := &Config{URL: "::not a valid url"}
		client, err := NewClient(cfg)
		require.Error(t, err)
		assert.Nil(t, client)
	})

	// 실패: NewClient 생성 시 AppRole 로그인이 실패하는 경우
	t.Run("NewClient fails on login error", func(t *testing.T) {
		cfg := &Config{
			URL:      mockServer.URL,
			RoleID:   "any-role",
			SecretID: "any-secret",
		}
		client, err := NewClient(cfg)
		require.Error(t, err)
		assert.Nil(t, client)
	})

	// 이후 실패 테스트를 위해, 로그인이 성공한 클라이언트를 미리 생성
	vaultCfg := api.DefaultConfig()
	vaultCfg.Address = mockServer.URL
	apiClient, _ := api.NewClient(vaultCfg)
	resp, _ := apiClient.Logical().Write("auth/approle/login-success", nil)
	apiClient.SetToken(resp.Auth.ClientToken)
	clientWithLogin := &Client{api: apiClient}

	// 실패: Secret을 찾지 못하는 경우 (404)
	t.Run("API calls fail on secret not found", func(t *testing.T) {
		assertAllApiCallsFail(t, clientWithLogin, "not-found-cluster")
	})

	// 실패: 응답 내용은 있으나 데이터가 비정상적인 경우
	t.Run("API calls fail on malformed response", func(t *testing.T) {
		assertAllApiCallsFail(t, clientWithLogin, "malformed-cluster")
	})
}

// ConfigFromEnv 함수의 독립적인 단위 테스트
func TestConfigFromEnv(t *testing.T) {
	t.Setenv("VAULT_URL", "http://test.vault:8200")
	t.Setenv("VAULT_ROLE_ID", "test-role")
	t.Setenv("VAULT_SECRET_ID", "test-secret")

	cfg := ConfigFromEnv()

	assert.Equal(t, "http://test.vault:8200", cfg.URL)
	assert.Equal(t, "test-role", cfg.RoleID)
	assert.Equal(t, "test-secret", cfg.SecretID)
}
