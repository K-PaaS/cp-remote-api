package router

import (
	"cp-remote-access-api/controller"

	"github.com/gin-gonic/gin"
)

func SetupRouter() *gin.Engine {
	r := gin.Default()
	r.Use(CORSMiddleware())

	health := r.Group("/actuator/health")
	{
		health.GET("", func(c *gin.Context) {
			c.String(200, "OK")
		})
		health.GET("/liveness", func(c *gin.Context) {
			c.String(200, "livez")
		})
		health.GET("/readiness", func(c *gin.Context) {
			c.String(200, "readyz")
		})
	}

	api := r.Group("/")
	api.Use(AuthMiddleware())
	{
		api.GET("/ws/exec", controller.ExecWebSocketHandler)
		api.GET("/shell/check", controller.CheckShellHandler)
	}

	return r
}

func Init() {
	r := SetupRouter()
	r.Run(":8080")
}
