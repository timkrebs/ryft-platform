// Package flags evaluates remote-config feature flags for one user (GET
// /v1/config, docs/DECISIONS.md D69). It is pure: flags and a user in, booleans
// out.
package flags

import (
	"crypto/sha256"
	"encoding/binary"
	"regexp"
	"slices"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// TTL is how long the app may cache an evaluation (GET /v1/config ttl_sec).
const TTL = 6 * 60 * 60

// keyPattern is a flag key: dotted lowercase segments, e.g. "train.enabled"
// (the same CHECK as migration 0014).
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ValidKey says whether key may name a flag.
func ValidKey(key string) bool { return len(key) <= 64 && keyPattern.MatchString(key) }

// Bucket places a user in 0–99 for a flag: the first four bytes of
// SHA-256("<key>:<user id>"), big-endian, modulo 100. Stable per user and
// flag, independent between flags, so raising rollout_pct only adds users.
func Bucket(user uuid.UUID, key string) int {
	sum := sha256.Sum256([]byte(key + ":" + user.String()))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}

// On says whether user gets flag f: it is enabled, and the user is on its
// allow-list or in its first rollout_pct buckets.
func On(f model.FeatureFlag, user uuid.UUID) bool {
	if !f.Enabled {
		return false
	}
	return slices.Contains(f.AllowUserIDs, user) || Bucket(user, f.Key) < f.RolloutPct
}

// Evaluate is every flag for one user.
func Evaluate(all []model.FeatureFlag, user uuid.UUID) map[string]bool {
	out := make(map[string]bool, len(all))
	for _, f := range all {
		out[f.Key] = On(f, user)
	}
	return out
}

// Enabled says whether user gets the flag key; a flag that does not exist is off.
func Enabled(all []model.FeatureFlag, key string, user uuid.UUID) bool {
	for _, f := range all {
		if f.Key == key {
			return On(f, user)
		}
	}
	return false
}
