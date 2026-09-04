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
	// Registered globally (not on a route group) so it also runs for the
	// browser's CORS preflight OPTIONS requests: Gin only invokes a route
	// group's .Use() middleware for methods that group actually registers a
	// handler for, and no OPTIONS handler is registered below. A group-scoped
	// CORS middleware never sees a real preflight -- it falls through to
	// Gin's default 404 (no CORS headers), which the browser reports to the
	// caller as a generic network failure. Global engine middleware, by
	// contrast, is part of the handler chain Gin uses even for unmatched
	// routes, so it reliably intercepts and answers every OPTIONS request.
	r.Use(middleware.CORS(h.Cfg))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	requireAuth := middleware.RequireAuth(h.Cfg.JWTSecret)

	authGroup := r.Group("/auth")
	{
		authGroup.POST("/signup", h.Signup)
		authGroup.POST("/login", middleware.LoginRateLimit(h.Redis), h.Login)
		authGroup.POST("/refresh", h.Refresh)
		authGroup.POST("/logout", h.Logout)
		authGroup.POST("/accept-invite", h.AcceptInvite)
		authGroup.GET("/me", requireAuth, h.Me)
	}

	// Team/plan management -- the dashboard's "Configuración" tab. Every
	// route requires auth; user/plan *mutations* additionally require a
	// minimum role (see internal/auth/roles.go).
	accountGroup := r.Group("/account")
	accountGroup.Use(requireAuth)
	{
		accountGroup.GET("/users", h.ListUsers)
		accountGroup.POST("/users/invite", middleware.RequireRole("admin"), h.InviteUser)
		accountGroup.PATCH("/users/:id/role", middleware.RequireRole("admin"), h.UpdateUserRole)
		accountGroup.DELETE("/users/:id", middleware.RequireRole("admin"), h.RemoveUser)
		accountGroup.GET("/plan", h.GetPlan)
		accountGroup.PATCH("/plan", middleware.RequireRole("owner"), h.UpdatePlan)
		accountGroup.GET("/events", h.ListEvents)
	}

	// Suggestions -- the per-view "Sugerencias" button and the global
	// "Sugerencias" tab in the dashboard. Viewers can read; acting on a
	// suggestion (generating a new one, changing its status) requires editor+.
	suggestionsGroup := r.Group("/suggestions")
	suggestionsGroup.Use(requireAuth)
	{
		suggestionsGroup.GET("", h.ListSuggestions)
		suggestionsGroup.POST("", middleware.RequireRole("editor"), h.CreateSuggestion)
		suggestionsGroup.PATCH("/:id/status", middleware.RequireRole("editor"), h.UpdateSuggestionStatus)
	}

	return r
}
