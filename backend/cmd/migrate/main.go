// migrate applies the database migrations as the schema owner, loads the
// built-in exercises and plans, and sets the passwords of the services'
// least-privilege roles where they changed. It runs once per deploy,
// before the services (a Kubernetes Job in ryft-migrate, a one-shot compose
// service); the services themselves cannot change the schema.
//
//	DATABASE_URL          the owner (postgres on Supabase)
//	DB_PASSWORD_INGEST    password of ryft_ingest, likewise _SCORING, _API and _NOTIFY
package main

import (
	"context"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
)

func main() {
	log := httpx.Logger("migrate")
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	pool, err := db.Connect(ctx, url, 2)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	// Built-in exercises and plans (Train), idempotent.
	if err := db.Seed(ctx, pool, log); err != nil {
		log.Error("seed", "err", err)
		os.Exit(1)
	}

	services := make([]string, 0, len(db.ServiceRoles))
	for s := range db.ServiceRoles {
		services = append(services, s)
	}
	slices.Sort(services)
	for _, s := range services {
		role, env := db.ServiceRoles[s], "DB_PASSWORD_"+strings.ToUpper(s)
		pw := os.Getenv(env)
		if pw == "" {
			// The role keeps its current password (or stays NOLOGIN): the
			// service fails to connect rather than falling back to the owner.
			log.Warn("no password given; role left unchanged", "role", role, "env", env)
			continue
		}
		changed, err := db.EnsureRolePassword(ctx, pool, url, role, pw)
		if err != nil {
			log.Error("set role password", "role", role, "err", err)
			os.Exit(1)
		}
		if changed {
			log.Info("role password set", "role", role)
		} else {
			log.Info("role password unchanged", "role", role)
		}
	}
	log.Info("schema current")
}
