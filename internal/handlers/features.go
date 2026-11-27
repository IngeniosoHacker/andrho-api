package handlers

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// Features by account ("roles" of the product): a feature shows up in the
// dashboard and in the ERP only when the plan includes it (bought) and the
// business uses it. For live sales:
//
//	available = plan has live_sale (Plataforma and up)
//	enabled   = available AND (the owner turned it on in Configuración,
//	            or, if they never chose, the business type is en_vivo)
//
// The ERP follows: andrho_live_sale's menu and models belong to a group
// AndRho grants every user while enabled and takes away when not.

const featureLiveSale = "live_sale"

// featureState is a feature for the dashboard.
type featureState struct {
	Available bool `json:"available"`
	Enabled   bool `json:"enabled"`
}

func (h *Handler) liveFeature(ctx context.Context, accountID string) (featureState, error) {
	caps, err := h.accountCaps(ctx, accountID)
	if err != nil {
		return featureState{}, err
	}
	st := featureState{Available: caps.LiveSale}
	if !st.Available {
		return st, nil
	}
	on, set, err := db.FeatureSetting(ctx, h.Accounts, accountID, featureLiveSale)
	if err != nil {
		return st, err
	}
	if set {
		st.Enabled = on
	} else {
		bt, _ := db.BusinessType(ctx, h.Accounts, accountID)
		st.Enabled = bt == "en_vivo"
	}
	return st, nil
}

// CRM from the lives (2026-11-19), both inside live sales:
//
//	live_crm       buyers and prospects in the ERP's contacts; on by
//	               default while live sales are on
//	live_whatsapp  the "Chatear por WhatsApp" button and the channel
//	               question, on the store's own number; offered only once
//	               the store connected it (whatsapp_business.go), and then
//	               on by default. In the test mode (LIVE_WHATSAPP_TEST,
//	               never in production) it can use AndRho's number, off
//	               by default
const (
	featureLiveCRM      = "live_crm"
	featureLiveWhatsApp = "live_whatsapp"
)

func (h *Handler) subFeature(ctx context.Context, accountID, feature string, available, def bool) (featureState, error) {
	st := featureState{Available: available}
	if !available {
		return st, nil
	}
	on, set, err := db.FeatureSetting(ctx, h.Accounts, accountID, feature)
	if err != nil {
		return st, err
	}
	st.Enabled = def
	if set {
		st.Enabled = on
	}
	return st, nil
}

// liveCRMFeature: available with live sales on.
func (h *Handler) liveCRMFeature(ctx context.Context, accountID string) (featureState, error) {
	live, err := h.liveFeature(ctx, accountID)
	if err != nil {
		return featureState{}, err
	}
	return h.subFeature(ctx, accountID, featureLiveCRM, live.Enabled, true)
}

// liveWhatsAppFeature: available with live sales on and a number for the
// buyers: the store's own (on by default) or, testing, AndRho's.
func (h *Handler) liveWhatsAppFeature(ctx context.Context, accountID string) (featureState, error) {
	live, err := h.liveFeature(ctx, accountID)
	if err != nil {
		return featureState{}, err
	}
	if !live.Enabled {
		return featureState{}, nil
	}
	line, ok := h.buyerLine(ctx, accountID)
	return h.subFeature(ctx, accountID, featureLiveWhatsApp, ok, ok && line.own())
}

func (h *Handler) featureOn(ctx context.Context, accountID, feature string) bool {
	var st featureState
	var err error
	switch feature {
	case featureLiveCRM:
		st, err = h.liveCRMFeature(ctx, accountID)
	case featureLiveWhatsApp:
		st, err = h.liveWhatsAppFeature(ctx, accountID)
	default:
		st, err = h.liveFeature(ctx, accountID)
	}
	return err == nil && st.Enabled
}

// accountFeatures is what GET /auth/me carries.
func (h *Handler) accountFeatures(ctx context.Context, accountID string) map[string]featureState {
	out := map[string]featureState{}
	if st, err := h.liveFeature(ctx, accountID); err == nil {
		out[featureLiveSale] = st
	}
	if st, err := h.liveCRMFeature(ctx, accountID); err == nil {
		out[featureLiveCRM] = st
	}
	if st, err := h.liveWhatsAppFeature(ctx, accountID); err == nil {
		out[featureLiveWhatsApp] = st
	}
	if st, err := h.workforceFeature(ctx, accountID); err == nil {
		out[featureWorkforce] = st
	}
	return out
}

// syncLiveERP shows or hides live sales in the account's ERP to match the
// feature (only where the module is installed). Best effort: it runs after
// the change that caused it, in the background.
func (h *Handler) syncLiveERP(accountID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := h.liveFeature(ctx, accountID)
		if err != nil {
			return
		}
		cl, err := h.odooFor(ctx, accountID)
		if err != nil || !cl.HasModel(ctx, liveModel) {
			return
		}
		if err := cl.Exec(ctx, liveModel, "andrho_set_enabled", []any{st.Enabled}, nil, nil); err != nil {
			log.Printf("features: live sales in %s's ERP: %v", accountID, err)
		}
	}()
}

// SetAccountFeature handles PUT /account/features/:feature (admin):
// {enabled}. Turning on needs the plan to include it.
func (h *Handler) SetAccountFeature(c *gin.Context) {
	claims, _ := currentClaims(c)
	feature := c.Param("feature")
	if feature == featureLiveCRM || feature == featureLiveWhatsApp {
		h.setSubFeature(c, feature)
		return
	}
	if feature == featureWorkforce {
		h.setWorkforceFeature(c)
		return
	}
	if feature != featureLiveSale {
		respondError(c, http.StatusNotFound, "función desconocida")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		respondError(c, http.StatusBadRequest, "enabled es obligatorio")
		return
	}
	ctx := c.Request.Context()
	st, err := h.liveFeature(ctx, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if *req.Enabled && !st.Available {
		c.JSON(http.StatusForbidden, gin.H{"error": "plan", "message": "La venta en vivo viene desde el plan Plataforma."})
		return
	}
	if err := db.SetFeature(ctx, h.Accounts, claims.AccountID, featureLiveSale, *req.Enabled); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	label := map[bool]string{true: "activada", false: "desactivada"}[*req.Enabled]
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "feature_changed", "Venta en vivo "+label)
	h.syncLiveERP(claims.AccountID)
	c.JSON(http.StatusOK, gin.H{"features": h.accountFeatures(ctx, claims.AccountID)})
}

var featureLabels = map[string]string{featureLiveCRM: "Clientes y prospectos de los lives", featureLiveWhatsApp: "WhatsApp en la venta en vivo"}

// setSubFeature turns live_crm or live_whatsapp on or off.
func (h *Handler) setSubFeature(c *gin.Context, feature string) {
	claims, _ := currentClaims(c)
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		respondError(c, http.StatusBadRequest, "enabled es obligatorio")
		return
	}
	ctx := c.Request.Context()
	var st featureState
	var err error
	if feature == featureLiveCRM {
		st, err = h.liveCRMFeature(ctx, claims.AccountID)
	} else {
		st, err = h.liveWhatsAppFeature(ctx, claims.AccountID)
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if *req.Enabled && !st.Available {
		msg := "Primero activa la venta en vivo."
		if feature == featureLiveWhatsApp {
			msg = "WhatsApp en la venta en vivo está en prueba y todavía no está disponible para tu cuenta."
		}
		c.JSON(http.StatusForbidden, gin.H{"error": "unavailable", "message": msg})
		return
	}
	if err := db.SetFeature(ctx, h.Accounts, claims.AccountID, feature, *req.Enabled); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	label := map[bool]string{true: "activado", false: "desactivado"}[*req.Enabled]
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "feature_changed", featureLabels[feature]+" "+label)
	c.JSON(http.StatusOK, gin.H{"features": h.accountFeatures(ctx, claims.AccountID)})
}

// setWorkforceFeature turns Personal en obra on or off (plan permitting).
func (h *Handler) setWorkforceFeature(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		respondError(c, http.StatusBadRequest, "enabled es obligatorio")
		return
	}
	ctx := c.Request.Context()
	st, err := h.workforceFeature(ctx, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if *req.Enabled && !st.Available {
		c.JSON(http.StatusForbidden, gin.H{"error": "plan", "message": "Personal en obra viene en los planes En órbita y Galaxy."})
		return
	}
	if err := db.SetFeature(ctx, h.Accounts, claims.AccountID, featureWorkforce, *req.Enabled); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	label := map[bool]string{true: "activado", false: "desactivado"}[*req.Enabled]
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "feature_changed", "Personal en obra "+label)
	c.JSON(http.StatusOK, gin.H{"features": h.accountFeatures(ctx, claims.AccountID)})
}
