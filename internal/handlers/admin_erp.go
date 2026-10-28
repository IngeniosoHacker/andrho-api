package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/analyzer"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
)

// AdminProvisionERP handles POST /admin/accounts/:site_id/erp: the ERP of
// an account that has none — accounts from before provisioning existed
// (AndRho's own, ANDRHO_SITE_ID) or whose job gave up after its retries.
// It runs the same job as the signup (internal/provision): progress in the
// dashboard, credentials and SSO secret saved. It refuses when the account
// already has its ERP or one is being created: provisioning a client again
// resets its admin password and SSO secret (VPS-MANUAL.md §12).
func (h *Handler) AdminProvisionERP(c *gin.Context) {
	ctx := c.Request.Context()
	acc, err := db.GetAccountBySiteID(ctx, h.Accounts, c.Param("site_id"))
	if errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusNotFound, "no account with that site_id")
		return
	} else if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := db.GetOdooConnection(ctx, h.Accounts, acc.ID); err == nil {
		respondError(c, http.StatusConflict, "this account already has its ERP")
		return
	} else if !errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	jobs, err := db.ListProvisioningJobs(ctx, h.Accounts, acc.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	var job *db.ProvisioningJob
	for i, j := range jobs {
		if j.Kind != "erp" {
			continue
		}
		switch j.Status {
		case provision.StatusWaitingVPS, provision.StatusError:
			job = &jobs[i]
		default: // pendiente, en_proceso, listo
			c.JSON(http.StatusConflict, gin.H{"error": "this account's ERP job is " + j.Status, "job": j})
			return
		}
	}

	pacc, err := h.ProvisionAccount(ctx, acc.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if job == nil {
		// The modules the account picked at signup, if it went through it.
		var modules []string
		if setup, err := db.GetAccountSetup(ctx, h.Accounts, acc.ID); err == nil {
			modules = setup.Modules
		}
		created, err := h.Provision.Plan(ctx, pacc, provision.Request{OdooModules: analyzer.OdooModules(modules)}, provision.Brief{})
		if err != nil || len(created) == 0 {
			respondError(c, http.StatusInternalServerError, "could not create the ERP job")
			return
		}
		job = &created[0]
	}
	h.Provision.RunAll(pacc, []db.ProvisioningJob{*job})
	c.JSON(http.StatusAccepted, gin.H{"site_id": acc.SiteID, "job": job})
}
