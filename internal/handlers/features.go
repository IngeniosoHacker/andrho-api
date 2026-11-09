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

// accountFeatures is what GET /auth/me carries.
func (h *Handler) accountFeatures(ctx context.Context, accountID string) map[string]featureState {
	out := map[string]featureState{}
	if st, err := h.liveFeature(ctx, accountID); err == nil {
		out[featureLiveSale] = st
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
	if c.Param("feature") != featureLiveSale {
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
