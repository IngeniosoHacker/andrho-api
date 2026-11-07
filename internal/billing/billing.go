// Package billing is the checkout (todo.md C5): one function, Checkout,
// prices what the business picked (internal/plans) and asks a
// PaymentProvider to charge it. Today there's no payment: PAYMENTS_ENABLED
// is false and NoopPaymentProvider approves everything, so the account is
// created with billing_status 'pending_payment' and payment_skipped = true.
// Turning payments on is implementing PaymentProvider and returning it from
// FromEnv; neither the signup nor the UI change.
package billing

import (
	"context"
	"log"
	"os"

	"github.com/IngeniosoHacker/andrho-api/internal/plans"
)

// Selection is what the business picked: a plan and its add-ons.
type Selection struct {
	Plan   string            `json:"plan"`
	Addons []plans.Selection `json:"addons"`
}

// Charge is a provider's answer.
type Charge struct {
	Approved bool
	// Skipped: nothing was charged (payments off).
	Skipped   bool
	Reference string
	Provider  string
}

// PaymentProvider charges a quote for an account.
type PaymentProvider interface {
	Name() string
	Charge(ctx context.Context, accountID string, q plans.Quote) (Charge, error)
}

// NoopPaymentProvider approves everything without charging.
type NoopPaymentProvider struct{}

// Name implements PaymentProvider.
func (NoopPaymentProvider) Name() string { return "noop" }

// Charge implements PaymentProvider.
func (NoopPaymentProvider) Charge(context.Context, string, plans.Quote) (Charge, error) {
	return Charge{Approved: true, Skipped: true, Provider: "noop"}, nil
}

// Enabled reports PAYMENTS_ENABLED.
func Enabled() bool { return os.Getenv("PAYMENTS_ENABLED") == "true" }

// FromEnv returns the provider to use. There is no real one yet: with
// PAYMENTS_ENABLED=true it warns and still skips the payment.
func FromEnv() PaymentProvider {
	if Enabled() {
		log.Println("billing: PAYMENTS_ENABLED=true pero no hay proveedor de pago; las cuentas se crean sin cobrar")
	}
	return NoopPaymentProvider{}
}

// Result is a priced and charged selection.
type Result struct {
	Quote  plans.Quote
	Charge Charge
	// Status for account_plan.billing_status.
	Status string
	Caps   plans.Capabilities
}

// Price validates and prices a selection (before the account exists).
func Price(sel Selection) (plans.Quote, error) {
	return plans.CalculateAddonPrice(sel.Plan, sel.Addons)
}

// Checkout prices sel and charges it with p. The caller stores the result
// (account_plan) and applies the capabilities.
func Checkout(ctx context.Context, p PaymentProvider, accountID string, sel Selection) (Result, error) {
	q, err := Price(sel)
	if err != nil {
		return Result{}, err
	}
	ch, err := p.Charge(ctx, accountID, q)
	if err != nil {
		return Result{Quote: q, Status: "failed"}, err
	}
	r := Result{Quote: q, Charge: ch, Caps: plans.Effective(q.Plan, sel.Addons)}
	switch {
	case ch.Skipped:
		r.Status = "pending_payment"
	case ch.Approved:
		r.Status = "active"
	default:
		r.Status = "failed"
	}
	return r, nil
}
