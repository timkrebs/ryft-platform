// Package logcheck owns the test-side half of "no health values in logs": it checks captured log lines against the keys each message may carry.
//
// A service's log test lists, per message, the keys the line may carry and the
// kind of value each key holds; Check reports any other message, key or value
// shape. It is for tests only and imports nothing internal: depguard fails the
// lint job when production code imports it (D114).
//
// A kind is a type and, where cheap, a shape: a UUID, a YYYY-MM-DD day, a member
// of a closed enum, a non-negative integer, a boolean. A recovery score logged
// under a count key (a float, or a number out of the count's bound) or under a
// renamed key fails.
//
// What a kind cannot do: tell an integer health value from an integer count
// under the same count key; only a bound helps there, and only where one is
// known. Text (an error's cause) is the other gap: the tests' scans for the
// request's own values (Forbidden) cover it.
//
// See docs/OPERATIONS.md → Data protection (logs); D114.
package logcheck
