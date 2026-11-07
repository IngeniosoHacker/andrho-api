package billing

import (
	"context"
	"testing"

	"github.com/IngeniosoHacker/andrho-api/internal/plans"
)

type approving struct{}

func (approving) Name() string { return "test" }
func (approving) Charge(context.Context, string, plans.Quote) (Charge, error) {
	return Charge{Approved: true, Reference: "ch_1", Provider: "test"}, nil
}

func TestCheckoutWithoutPayments(t *testing.T) {
	r, err := Checkout(context.Background(), NoopPaymentProvider{}, "acc", Selection{Plan: "plataforma",
		Addons: []plans.Selection{{FeatureID: "sitio_web_basico"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "pending_payment" || !r.Charge.Skipped || r.Quote.Total != 549 || !r.Caps.Website {
		t.Fatalf("%+v", r)
	}
	if _, err := Checkout(context.Background(), NoopPaymentProvider{}, "acc", Selection{Plan: "plataforma",
		Addons: []plans.Selection{{FeatureID: "tienda_en_linea"}}}); err == nil {
		t.Fatal("an add-on two plans up was accepted")
	}
}

// A real provider plugs in without touching the checkout.
func TestCheckoutWithProvider(t *testing.T) {
	r, err := Checkout(context.Background(), approving{}, "acc", Selection{Plan: "despegue"})
	if err != nil || r.Status != "active" || r.Charge.Skipped || r.Charge.Reference != "ch_1" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("PAYMENTS_ENABLED", "false")
	if _, ok := FromEnv().(NoopPaymentProvider); !ok || Enabled() {
		t.Fatal("noop expected")
	}
}
