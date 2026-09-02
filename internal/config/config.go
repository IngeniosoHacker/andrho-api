// Package config loads runtime configuration from environment variables.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds every environment-driven setting the service needs.
type Config struct {
	Port                string
	DatabaseURL         string
	TrackerDatabaseURL  string
	RedisURL            string
	JWTSecret           string
	JWTAccessTTLMinutes int
	JWTRefreshTTLDays   int
	AllowedOrigins      []string // empty slice or containing "*" means "allow all"
}

// Load reads a .env file if present (silently ignored when missing, since in
// production Railway injects env vars directly) and returns the parsed Config.
func Load() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("config: no .env file found, relying on process environment")
	}

	cfg := &Config{
		Port:                getEnv("PORT", "8080"),
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		TrackerDatabaseURL:  getEnv("TRACKER_DATABASE_URL", ""),
		RedisURL:            getEnv("REDIS_URL", ""),
		JWTSecret:           getEnv("JWT_SECRET", ""),
		JWTAccessTTLMinutes: getEnvInt("JWT_ACCESS_TTL_MINUTES", 30),
		JWTRefreshTTLDays:   getEnvInt("JWT_REFRESH_TTL_DAYS", 30),
		AllowedOrigins:      parseOrigins(getEnv("ALLOWED_ORIGINS", "*")),
	}

	return cfg
}

// AllowAllOrigins reports whether CORS should allow every origin (dev default).
func (c *Config) AllowAllOrigins() bool {
	if len(c.AllowedOrigins) == 0 {
		return true
	}
	for _, o := range c.AllowedOrigins {
		if o == "*" {
			return true
		}
	}
	return false
}

func parseOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: invalid int for %s=%q, using default %d", key, v, fallback)
		return fallback
	}
	return n
}
