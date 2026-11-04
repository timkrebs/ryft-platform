// Package testsupport holds the helpers the Postgres-backed tests of several
// packages share.
//
// It must not be imported by non-test code, and it imports no other package of
// this module, so any test (including package-internal ones such as internal/db)
// can use it without an import cycle.
package testsupport
