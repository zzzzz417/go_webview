package routers

import (
	"main/internal/api"
	staticassets "main/static"
	"net/http"

	"github.com/gin-gonic/gin"
)

// InitRouter initialize routing information
func InitRouter() *gin.Engine {
	rt := gin.New()
	rt.Use(gin.Recovery())
	rt.Use(gin.Logger())

	api.RegisterKJYYRoutes(rt.Group("/api"))
	rt.GET("/", serveStaticString(staticassets.IndexHTML, "text/html; charset=utf-8"))
	rt.GET("/index.html", serveStaticString(staticassets.IndexHTML, "text/html; charset=utf-8"))
	rt.GET("/app.js", serveStaticString(staticassets.AppJS, "application/javascript; charset=utf-8"))
	rt.GET("/styles.css", serveStaticString(staticassets.StylesCSS, "text/css; charset=utf-8"))

	return rt
}

func serveStaticString(content, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if content == "" {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, contentType, []byte(content))
	}
}
