package provision

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/sites"
)

// The client's website (todo.md A3.4). Every step is idempotent and saves
// its result in account_sites, so SiteLoop can call AdvanceSite again and
// again until the site is live:
//
//  1. reserve the subdomain (EnsureSite / OfferSite)
//  2. create site-<account_id> in the Clients project
//  3. configure it (variables, root directory) and deploy the template
//  4. once deployed, remove any *.up.railway.app domain (no duplicate
//     content for Google), domain_state = wildcard, status = live
//
// sites-router serves a site only once it's live; before that it answers
// 503 with Retry-After.

// subdomainCandidates are the names tried for a business, in order.
func subdomainCandidates(company, clientID string) []string {
	out := sites.Candidates(sites.Normalize(company), 20)
	if clientID != "" {
		out = append(out, sites.Normalize(clientID))
	}
	return out
}

func (p *Provisioner) host(sub string) string {
	h, err := p.Sites.Host(sub)
	if err != nil {
		return sub
	}
	return h
}

// reserve gives the account its subdomain, starting with status.
func (p *Provisioner) reserve(ctx context.Context, acc Account, status string) (db.AccountSite, error) {
	return db.ReserveSiteSubdomain(ctx, p.DB, acc.ID, status, subdomainCandidates(acc.Company, acc.ClientID), p.host)
}

// OfferSite records the site the plan doesn't include yet (todo.md B1):
// the subdomain is kept, no service is created, and the site can be turned
// on later (PATCH /account/plan) without repeating the signup.
func (p *Provisioner) OfferSite(ctx context.Context, acc Account) (db.AccountSite, error) {
	return p.reserve(ctx, acc, db.SiteOffered)
}

// EnsureSite reserves the subdomain and marks the site as deploying (an
// offered site becomes a real one).
func (p *Provisioner) EnsureSite(ctx context.Context, acc Account) (db.AccountSite, error) {
	s, err := p.reserve(ctx, acc, db.SiteDeploying)
	if err != nil {
		return s, err
	}
	if s.Status != db.SiteDeploying && s.Status != db.SiteLive {
		s.Status = db.SiteDeploying
		if err := db.SaveAccountSite(ctx, p.DB, s); err != nil {
			return s, err
		}
	}
	return s, nil
}

// AdvanceSite moves the site one step closer to live, recording where it
// is in domain_state. It never returns an error: what failed is written
// there for staff, and SiteLoop tries again.
func (p *Provisioner) AdvanceSite(ctx context.Context, acc Account, s db.AccountSite) db.AccountSite {
	save := func(state string) db.AccountSite {
		s.DomainState = state
		if err := db.SaveAccountSite(ctx, p.DB, s); err != nil {
			log.Printf("provision: site %s: %v", acc.ClientID, err)
		}
		return s
	}
	fail := func(step string, err error) db.AccountSite {
		log.Printf("provision: site %s %s: %v", acc.ClientID, step, err)
		return save("error (" + step + "): " + err.Error())
	}
	if s.Status != db.SiteDeploying {
		return s
	}
	cfg := p.Sites
	if !cfg.Railway() {
		return save("pendiente: crear " + sites.ServiceName(acc.ID) + " en el proyecto Clients de Railway (faltan " + cfg.Missing() + ")")
	}
	if s.RailwayServiceID == "" {
		id, name, err := cfg.EnsureService(ctx, acc.ID)
		if err != nil {
			return fail("crear servicio", err)
		}
		s.RailwayServiceID, s.RailwayServiceName = id, name
		s = save("servicio creado")
	}
	d, err := cfg.LatestDeployment(ctx, s.RailwayServiceID)
	if err != nil {
		return fail("leer despliegue", err)
	}
	switch {
	case d.Status == "":
		if err := cfg.ConfigureService(ctx, s.RailwayServiceID, acc.ClientID); err != nil {
			return fail("configurar servicio", err)
		}
		if err := cfg.Deploy(ctx, s.RailwayServiceID, d.Connected); err != nil {
			return fail("desplegar", err)
		}
		return save("desplegando")
	case d.Failed():
		return save("error: el despliegue terminó en " + d.Status + "; revisar el servicio " + s.RailwayServiceName + " en Railway")
	case !d.Done():
		return save("desplegando (" + d.Status + ")")
	}
	if _, err := cfg.RemovePublicDomains(ctx, s.RailwayServiceID); err != nil {
		return fail("quitar dominio público", err)
	}
	state := "wildcard"
	if !cfg.Wildcard {
		// The old per-host setup (Railway domain + Porkbun), kept for a
		// client's own domain.
		st, err := cfg.Setup(ctx, s.PublicHost())
		if err != nil {
			return fail("dominio", err)
		}
		state = st
	}
	s.Status = db.SiteLive
	_ = db.LogEvent(ctx, p.DB, uuid.NewString(), acc.ID, nil, "site_live", "Tu sitio está en línea: https://"+s.PublicHost())
	return save(state)
}

// MigrateLegacySites moves sites of the old scheme (web.<id>.andrho.com on a
// shared service) to the wildcard: each gets a subdomain (its site_id when
// free), keeps the old host to answer with a 301, and is provisioned like a
// new site. Safe on every boot.
func (p *Provisioner) MigrateLegacySites(ctx context.Context) (int, error) {
	list, err := db.LegacySites(ctx, p.DB)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range list {
		acc, err := db.GetAccountByID(ctx, p.DB, s.AccountID)
		if err != nil {
			continue
		}
		cands := append([]string{sites.Normalize(acc.SiteID)}, subdomainCandidates(acc.CompanyName, "")...)
		for _, sub := range cands {
			err = db.MoveLegacySite(ctx, p.DB, s.AccountID, sub, p.host(sub), db.SiteDeploying)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				continue
			}
			break
		}
		if err != nil {
			return n, fmt.Errorf("provision: migrate site %s: %w", acc.SiteID, err)
		}
		n++
	}
	return n, nil
}

// SiteLoop advances every deploying site each interval (Railway builds take
// minutes) and, once at start, migrates the old sites.
func (p *Provisioner) SiteLoop(ctx context.Context, interval time.Duration, accountFor func(context.Context, string) (Account, error)) {
	if n, err := p.MigrateLegacySites(ctx); err != nil {
		log.Printf("provision: legacy sites: %v", err)
	} else if n > 0 {
		log.Printf("provision: %d site(s) moved to the wildcard", n)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		list, err := db.SitesToProvision(ctx, p.DB)
		if err != nil {
			log.Printf("provision: sites: %v", err)
		}
		for _, s := range list {
			acc, err := accountFor(ctx, s.AccountID)
			if err != nil {
				continue
			}
			p.AdvanceSite(ctx, acc, s)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
