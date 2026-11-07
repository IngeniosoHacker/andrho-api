// Command sites-wildcard is the one-time setup of todo.md A2: it adds
// *.web.andrho.com as a custom domain of sites-router on Railway and creates
// the records Railway asks for (the wildcard CNAME, the _acme-challenge
// CNAME for the certificate and the verification TXT) in Porkbun.
//
//	RAILWAY_API_TOKEN=… RAILWAY_ROUTER_SERVICE_ID=… RAILWAY_CLIENTS_ENVIRONMENT_ID=… \
//	PORKBUN_API_KEY=… PORKBUN_SECRET_KEY=… go run ./cmd/sites-wildcard [*.web.andrho.com]
//
// Without the Porkbun keys it prints the records to create by hand. Run it
// once; then check that https://cualquiercosa.web.andrho.com answers with a
// valid certificate (sites-router's 404 is the right answer).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/sites"
)

func main() {
	host := "*.web.andrho.com"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	cfg := sites.FromEnv()
	cfg.Wildcard = false // the per-host path: a domain on sites-router plus its DNS
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	state, err := cfg.Setup(ctx, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(host+":", state)
}
