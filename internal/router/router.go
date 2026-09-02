// Package router registers all Gin routes for andrho-api.
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/handlers"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
)

// New builds the Gin engine with every route wired up.
func New(h *handlers.Handler) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	authGroup := r.Group("/auth")
	authGroup.Use(middleware.CORS(h.Cfg))
	{
		authGroup.POST("/signup", h.Signup)
		authGroup.POST("/login", middleware.LoginRateLimit(h.Redis), h.Login)
		authGroup.POST("/refresh", h.Refresh)
		authGroup.POST("/logout", h.Logout)
		authGroup.GET("/me", middleware.RequireAuth(h.Cfg.JWTSecret), h.Me)
	}

	return r
}
