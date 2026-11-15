// Package pubsub owns events.Bus on Pub/Sub over REST, the push and tick handlers, and the topology that Terraform's and the emulator's files are written from.
//
// It uses the standard library only, no Google client library. Only eventbus and
// cmd/pubsub-topology import it: the services see events.Bus, an events.Handler and
// the handlers eventbus builds. It knows no table: notify's deferrals reach the
// outbox through Deferrer, and the outbox reaches Pub/Sub through events.Sender.
//
// Invariants:
//   - Publishing is one REST call per message (POST .../topics/<t>:publish) with an
//     access token from the metadata server, cached until a minute before it expires
//     and fetched again after a 401; to PUBSUB_EMULATOR_HOST it is plain http without
//     a token. A failure is the caller's to retry (the outbox's), and a key without a
//     topic is ErrNoTopic, never a silent drop. What no retry can deliver (no topic, an
//     ordered event without a user, Pub/Sub's 400, 403, 404, 413) is events.Rejected,
//     so the outbox sets it aside at once. Scoring's two topics carry the body's
//     user_id as ordering key, so Pub/Sub holds a user's later messages until the
//     current one is settled.
//   - Every /push/* and /internal/* request is checked first (auth.PushVerifier:
//     Google OIDC, or nothing only under ALLOW_INSECURE_PUSH). Neither the
//     Authorization header nor a body is ever logged; the last error of a message
//     is, with its messageId, because Pub/Sub's own dead letters cannot carry it. The
//     envelope is the pusher's: a messageId that is not decimal is a 400, and what
//     else is logged or stored from it is cut to a fixed length.
//   - The push path fixes the key: on /push/<key> a ryft-key that is not <key> is a
//     bad event filed under <key>, as is data that does not decode (events.Bad). A
//     bad event is published to the consumer's own dead-letter topic with ryft-error
//     and acked; if that publish fails the push is nacked, so Pub/Sub's dead-letter
//     policy (20 attempts) takes it. A handler's error is a nack, Permanent an ack
//     (dropped), and Defer an ack once the Deferrer stored the not-before row (a nack
//     without one). A delivery attempt above 1, or ryft-replayed=true, reaches the
//     handler as a redelivery; a missing attempt counts as 1. Every handler stays
//     idempotent (D100): Pub/Sub delivers at least once, also after an ack.
//   - A tick runs the service's steps in order within its budget and is answered 204
//     whatever they did; one that arrives while another runs in the process is
//     answered at once and counted as overlap.
//
// See docs/MESSAGING_DESIGN.md; D99, D100, D101, D218, D219.
package pubsub
