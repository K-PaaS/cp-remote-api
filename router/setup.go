package router

import (
	"cp-remote-access-api/controller"

	"github.com/gin-gonic/gin"
)

func Init() {
	r := gin.Default()
	r.Use(CORSMiddleware())

	r.GET("/livez", func(c *gin.Context) {
		c.String(200, "livez")
	})
	r.GET("/readyz", func(c *gin.Context) {
		c.String(200, "readyz")
	})

	api := r.Group("/")
	api.Use(AuthMiddleware())
	{
		api.GET("/ws/exec", controller.ExecWebSocketHandler)
		api.GET("/shell/check", controller.CheckShellHandler)
	}

	r.Run(":8080")
}
