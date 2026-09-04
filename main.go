// Command andrho-api is the accounts/auth backend for the AndRho ecosystem.
// See README.md for the full API contract.
package main

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/config"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/handlers"
	"github.com/IngeniosoHacker/andrho-api/internal/redisclient"
	"github.com/IngeniosoHacker/andrho-api/internal/router"
	"github.com/IngeniosoHacker/andrho-api/internal/trackerdb"
)

func main() {
	ctx := context.Background()
	cfg := config.Load()

	if cfg.JWTSecret == "" {
		log.Fatal("main: JWT_SECRET is required")
	}

	accountsPool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("main: connect accounts db: %v", err)
	}
	defer accountsPool.Close()

	if err := db.Migrate(ctx, accountsPool); err != nil {
		log.Fatalf("main: migrate accounts db: %v", err)
	}

	// One-time-per-account backfill: every account created before the
	// multi-user/roles migration gets an 'owner' user row generated from its
	// legacy email/password_hash. Idempotent, safe on every boot (see
	// db.BackfillOwnerUsers).
	if n, err := db.BackfillOwnerUsers(ctx, accountsPool, uuid.NewString); err != nil {
		log.Fatalf("main: backfill owner users: %v", err)
	} else if n > 0 {
		log.Printf("main: backfilled %d owner user(s) for pre-existing accounts", n)
	}

	trackerPool, err := trackerdb.New(ctx, cfg.TrackerDatabaseURL)
	if err != nil {
		log.Fatalf("main: connect tracker db: %v", err)
	}
	defer trackerPool.Close()

	redisClient, err := redisclient.New(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("main: connect redis: %v", err)
	}
	defer redisClient.Close()

	h := handlers.New(accountsPool, trackerPool, redisClient, cfg)
	r := router.New(h)

	addr := ":" + cfg.Port
	log.Printf("main: andrho-api listening on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("main: server error: %v", err)
	}
}
