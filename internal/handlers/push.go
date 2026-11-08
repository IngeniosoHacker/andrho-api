package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// Web Push (internal/notify): the browser asks for the public key, the
// service worker subscribes with it (web/public/sw.js) and the
// subscription is stored for the signed-in person.

// PushKey handles GET /push/key (public): the VAPID public key ("" when
// push is off, and the page doesn't offer it).
func (h *Handler) PushKey(c *gin.Context) {
	key := ""
	if h.Notify != nil && h.Notify.Push.Enabled() {
		key = h.Notify.Push.Public
	}
	c.JSON(http.StatusOK, gin.H{"key": key})
}

type pushRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// SubscribePush handles POST /account/push: this browser wants notices.
func (h *Handler) SubscribePush(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req pushRequest
	if err := c.ShouldBindJSON(&req); err != nil || !strings.HasPrefix(req.Endpoint, "https://") || len(req.Endpoint) > 1000 ||
		req.Keys.P256dh == "" || req.Keys.Auth == "" || len(req.Keys.P256dh) > 200 || len(req.Keys.Auth) > 100 {
		respondError(c, http.StatusBadRequest, "suscripción inválida")
		return
	}
	if err := db.SavePushSubscription(c.Request.Context(), h.Accounts, db.PushSubscription{UserID: claims.Sub, AccountID: claims.AccountID,
		Endpoint: req.Endpoint, P256dh: req.Keys.P256dh, Auth: req.Keys.Auth}); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UnsubscribePush handles DELETE /account/push: this browser stops.
func (h *Handler) UnsubscribePush(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req pushRequest
	_ = c.ShouldBindJSON(&req)
	_ = db.DeletePushSubscription(c.Request.Context(), h.Accounts, claims.Sub, req.Endpoint)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
