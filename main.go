package main

import (
	"cp-remote-access-api/config"
	"cp-remote-access-api/router"
)

func init() {
	config.InitEnvConfigs()
}
func main() {
	router.Init()
}
