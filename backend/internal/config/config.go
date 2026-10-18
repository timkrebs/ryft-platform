// Package config reads service configuration from the environment.
package config

import (
	"fmt"
	"os"

	"github.com/google/uuid"
)

// DefaultUserID is the seeded single-tenant user. Multi-user mode replaces the
// static API key with per-device keys that map to a user_id (see devices table).
var DefaultUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

type Config struct {
	ServiceName string
	Port        string
	DatabaseURL string
	NatsURL     string
	APIKey      string // shared secret the iOS app sends as Bearer token
	UserID      uuid.UUID
}

func Load(service string) (Config, error) {
	c := Config{
		ServiceName: service,
		Port:        getenv("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		NatsURL:     getenv("NATS_URL", "nats://nats:4222"),
		APIKey:      os.Getenv("API_KEY"),
		UserID:      DefaultUserID,
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if v := os.Getenv("USER_ID"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c, fmt.Errorf("USER_ID: %w", err)
		}
		c.UserID = id
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
