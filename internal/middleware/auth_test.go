package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
)

// A supervisor reaches only their session, their WhatsApp and Personal.
func TestSupervisorScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireAuth("s"))
	ok := func(c *gin.Context) { c.Status(200) }
	for _, p := range []string{"/workforce/staff", "/account/me/whatsapp", "/auth/me", "/erp/inventory", "/account/users", "/live/status"} {
		r.GET(p, ok)
	}
	r.POST("/workforce/sites", RequireRole("admin"), ok)
	tok := func(role string) string {
		s, _ := auth.IssueAccessToken("s", "u1", "a1", "x@y.z", "site", "Co", role, time.Hour)
		return s
	}
	for path, want := range map[string]int{"/workforce/staff": 200, "/account/me/whatsapp": 200, "/auth/me": 200,
		"/erp/inventory": 403, "/account/users": 403, "/live/status": 403} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok("supervisor"))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("supervisor %s: %d want %d", path, rec.Code, want)
		}
	}
	req := httptest.NewRequest("POST", "/workforce/sites", nil)
	req.Header.Set("Authorization", "Bearer "+tok("supervisor"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("supervisor creating a site: %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/erp/inventory", nil)
	req.Header.Set("Authorization", "Bearer "+tok("viewer"))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("viewer: %d", rec.Code)
	}
}
