package config

import (
	"github.com/spf13/viper"
	"log"
	"os"
)

var Env *envConfigs

func InitEnvConfigs() {
	Env = loadEnvVariables()
}

type envConfigs struct {
	ServerPort    string `mapstructure:"SERVER_PORT"`
	JwtSecret     string `mapstructure:"JWT_SECRET"`
	VaultUrl      string `mapstructure:"VAULT_URL"`
	VaultRoleId   string `mapstructure:"VAULT_ROLE_ID"`
	VaultSecretId string `mapstructure:"VAULT_SECRET_ID"`
}

func loadEnvVariables() (config *envConfigs) {
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
