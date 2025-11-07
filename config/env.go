package config

import (
	"log"
	"os"

	"github.com/spf13/viper"
)

var Env *EnvConfigs

func InitEnvConfigs() {
	Env = loadEnvVariables()
}

type EnvConfigs struct {
	ServerPort    string `mapstructure:"SERVER_PORT"`
	JwtSecret     string `mapstructure:"JWT_SECRET"`
	VaultUrl      string `mapstructure:"VAULT_URL"`
	VaultRoleId   string `mapstructure:"VAULT_ROLE_ID"`
	VaultSecretId string `mapstructure:"VAULT_SECRET_ID"`
}

func loadEnvVariables() (config *EnvConfigs) {
	wd, _ := os.Getwd()
	log.Print("working dir:", wd)
	viper.AddConfigPath(".")
	viper.SetConfigName("config")
	viper.SetConfigType("env")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatal("Error reading env file", err)
	}
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatal(err)
	}
	return
}
