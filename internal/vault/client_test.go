package vault

import (
	"cp-remote-access-api/config"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockVault: "성공" 시나리오용 가짜 Vault 서버 설정
func setupMockVault(t *testing.T) *httptest.Server {
	handler := http.NewServeMux()

	// 1. AppRole 로그인
	handler.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"auth":{"client_token":"test-token"}}`)
	})

	// 2. SUPER_ADMIN 경로
	handler.HandleFunc("/v1/secret/data/cluster/test-cluster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"data":{"clusterApiUrl":"https://fake-cluster.api","clusterToken":"super-admin-token"}}}`)
	})

	// 3. CLUSTER_ADMIN 경로
	handler.HandleFunc("/v1/secret/data/user/admin-guid/test-cluster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"data":{"clusterToken":"cluster-admin-token"}}}`)
	})

	// 4. USER 경로
	handler.HandleFunc("/v1/secret/data/user/user-guid/test-cluster/user-ns", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"data":{"clusterToken":"user-token"}}}`)
	})

	return httptest.NewServer(handler)
}

// setupFailingMockVault: "실패" 시나리오용 가짜 Vault 서버 설정
func setupFailingMockVault(t *testing.T) *httptest.Server {
	handler := http.NewServeMux()

	handler.HandleFunc("/v1/auth/approle/login", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	})
	handler.HandleFunc("/v1/auth/approle/login-success", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"auth":{"client_token":"test-token"}}`)
	})

	notFoundHandler := func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}
	handler.HandleFunc("/v1/secret/data/cluster/not-found", notFoundHandler)
	handler.HandleFunc("/v1/secret/data/user/any-user/not-found/any-ns", notFoundHandler)

	handler.HandleFunc("/v1/secret/data/cluster/malformed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data": {"wrong_key": "value"}}`)
	})
	handler.HandleFunc("/v1/secret/data/user/any-user/malformed/any-ns", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"wrong_key": "value"}`)
	})

	return httptest.NewServer(handler)
}

// TestVaultClient_Success: Vault 클라이언트 성공 시나리오
func TestVaultClient_Success(t *testing.T) {
	mockServer := setupMockVault(t)
	defer mockServer.Close()

	// Arrange: 환경 변수 설정
	t.Setenv("VAULT_URL", mockServer.URL)
	t.Setenv("VAULT_ROLE_ID", "test-role-id")
	t.Setenv("VAULT_SECRET_ID", "test-secret-id")

	// Arrange: config.Env 수동 초기화 (nil 패닉 방지)
	config.Env = &config.EnvConfigs{
		VaultUrl:      mockServer.URL,
		VaultRoleId:   "test-role-id",
		VaultSecretId: "test-secret-id",
	}

	// Arrange: 클라이언트 생성 (AppRole 로그인)
	cfg := ConfigFromEnv()
	client, err := NewClient(cfg)
	require.NoError(t, err, "NewClient should succeed")
	require.NotNil(t, client)

	// Act & Assert: GetClusterInfo
	t.Run("GetClusterInfo successfully", func(t *testing.T) {
		info, err := client.GetClusterInfo("test-cluster")
		assert.NoError(t, err)
		assert.Equal(t, "https://fake-cluster.api", info.APIServerURL)
		assert.Equal(t, "super-admin-token", info.BearerToken)
	})

	// Act & Assert: GetClusterAPI
	t.Run("GetClusterAPI successfully", func(t *testing.T) {
		apiURL, err := client.GetClusterAPI("test-cluster")
		assert.NoError(t, err)
		assert.Equal(t, "https://fake-cluster.api", apiURL)
	})

	// Act & Assert: GetClusterToken (SUPER_ADMIN)
	t.Run("GetClusterToken for SUPER_ADMIN", func(t *testing.T) {
		token, err := client.GetClusterToken("test-cluster", "any-user", "SUPER_ADMIN", "any-ns")
		assert.NoError(t, err)
		assert.Equal(t, "super-admin-token", token)
	})

	// Act & Assert: GetClusterToken (CLUSTER_ADMIN)
	t.Run("GetClusterToken for CLUSTER_ADMIN", func(t *testing.T) {
		token, err := client.GetClusterToken("test-cluster", "admin-guid", "CLUSTER_ADMIN", "any-ns")
		assert.NoError(t, err)
		assert.Equal(t, "cluster-admin-token", token)
	})

	// Act & Assert: GetClusterToken (USER)
	t.Run("GetClusterToken for USER", func(t *testing.T) {
		token, err := client.GetClusterToken("test-cluster", "user-guid", "USER", "user-ns")
		assert.NoError(t, err)
		assert.Equal(t, "user-token", token)
	})
}

// TestVaultClient_Failures: Vault 클라이언트 실패 시나리오
func TestVaultClient_Failures(t *testing.T) {
	mockServer := setupFailingMockVault(t)
	defer mockServer.Close()

	// Arrange: config.Env 수동 초기화 (nil 패닉 방지)
	config.Env = &config.EnvConfigs{}

	// 실패: NewClient (URL 포맷 오류)
	t.Run("NewClient fails on invalid URL", func(t *testing.T) {
		t.Setenv("VAULT_URL", "::not a valid url")

		config.Env.VaultUrl = "::not a valid url"

		cfg := ConfigFromEnv()
		client, err := NewClient(cfg)
		require.Error(t, err)
		assert.Nil(t, client)
	})

	// 실패: NewClient (AppRole 로그인 403)
	t.Run("NewClient fails on login error (403)", func(t *testing.T) {
		t.Setenv("VAULT_URL", mockServer.URL)
		config.Env.VaultUrl = mockServer.URL

		cfg := ConfigFromEnv()
		client, err := NewClient(cfg)
		require.Error(t, err, "NewClient should fail on AppRole login")
		assert.Contains(t, err.Error(), "permission denied")
		assert.Nil(t, client)
	})

	vaultCfg := api.DefaultConfig()
	vaultCfg.Address = mockServer.URL
	apiClient, _ := api.NewClient(vaultCfg)
	resp, _ := apiClient.Logical().Write("auth/approle/login-success", nil)
	apiClient.SetToken(resp.Auth.ClientToken)
	clientWithLogin := &Client{api: apiClient}

	// 실패: Secret Not Found (JSON 파싱 에러 -> if err != nil 검증)
	t.Run("API calls fail on secret not found (404)", func(t *testing.T) {
		_, err := clientWithLogin.GetClusterInfo("not-found")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "json: cannot unmarshal")

		_, err = clientWithLogin.GetClusterToken("not-found", "any-user", "USER", "any-ns")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "json: cannot unmarshal")
	})

	// 실패: 응답 데이터 구조 오류 (secret == nil 검증)
	t.Run("API calls fail on malformed response", func(t *testing.T) {
		_, err := clientWithLogin.GetClusterInfo("malformed")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed secret data")

		_, err = clientWithLogin.GetClusterToken("malformed", "any-user", "USER", "any-ns")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed secret data")
	})
}

// TestConfigFromEnv: ConfigFromEnv 함수가 환경변수 잘 읽는지 확인
func TestConfigFromEnv(t *testing.T) {
	// Arrange: config.Env 수동 초기화 (nil 패닉 방지)
	config.Env = &config.EnvConfigs{
		VaultUrl:      "http://test.vault:8200",
		VaultRoleId:   "test-role",
		VaultSecretId: "test-secret",
	}

	// Act
	cfg := ConfigFromEnv()

	// Assert
	assert.Equal(t, "http://test.vault:8200", cfg.URL)
	assert.Equal(t, "test-role", cfg.RoleID)
	assert.Equal(t, "test-secret", cfg.SecretID)
}
