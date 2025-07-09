package vault

import (
	"cp-remote-access-api/model"
	"fmt"
	"os"

	"github.com/hashicorp/vault/api"
)

type Config struct {
	URL      string
	RoleID   string
	SecretID string
}

func ConfigFromEnv() *Config {
	return &Config{
		URL:      os.Getenv("VAULT_URL"),
		RoleID:   os.Getenv("VAULT_ROLE_ID"),
		SecretID: os.Getenv("VAULT_SECRET_ID"),
	}
}

type Client struct {
	api *api.Client
}

func NewClient(cfg *Config) (*Client, error) {
	vaultCfg := api.DefaultConfig()
	vaultCfg.Address = cfg.URL

	client, err := api.NewClient(vaultCfg)
	if err != nil {
		return nil, err
	}

	resp, err := client.Logical().Write("auth/approle/login", map[string]interface{}{
		"role_id":   cfg.RoleID,
		"secret_id": cfg.SecretID,
	})
	if err != nil {
		return nil, err
	}
	client.SetToken(resp.Auth.ClientToken)

	return &Client{api: client}, nil
}

func (c *Client) GetClusterInfo(clusterID string) (model.ClusterCredential, error) {

	var info model.ClusterCredential

	path := fmt.Sprintf("secret/data/cluster/%s", clusterID)
	secret, err := c.api.Logical().Read(path)
	if err != nil || secret == nil || secret.Data == nil || secret.Data["data"] == nil {
		return model.ClusterCredential{}, err
	}
	data := secret.Data["data"].(map[string]interface{})
	info = model.ClusterCredential{
		ClusterID:    clusterID,
		APIServerURL: data["clusterApiUrl"].(string),
		BearerToken:  data["clusterToken"].(string),
	}
	return info, nil
}

func (c *Client) GetClusterAPI(clusterID string) (string, error) {
	path := fmt.Sprintf("secret/data/cluster/%s", clusterID)
	secret, err := c.api.Logical().Read(path)
	if err != nil || secret == nil || secret.Data == nil || secret.Data["data"] == nil {
		return "", err
	}
	data := secret.Data["data"].(map[string]interface{})
	return data["clusterApiUrl"].(string), nil
}
func (c *Client) GetClusterToken(clusterID string, userGuid string, userType string) (string, error) {
	var path string
	if userType == "SUPER_ADMIN" {
		path = fmt.Sprintf("secret/data/cluster/%s", clusterID)
	} else {
		path = fmt.Sprintf("secret/data/user/%s/%s", userGuid, clusterID)
	}

	secret, err := c.api.Logical().Read(path)
	if err != nil || secret == nil || secret.Data == nil || secret.Data["data"] == nil {
		return "", err
	}
	data := secret.Data["data"].(map[string]interface{})
	return data["clusterToken"].(string), nil
}
