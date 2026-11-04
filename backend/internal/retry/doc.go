// Package retry owns start-up retry with backoff and jitter for dependencies that
// may not be up yet.
//
// Those are the database before the migrate job has set the role passwords, and
// RabbitMQ before its nodes are ready. It must not wrap a request-path call; it is for start-up only. Exiting and
// letting Kubernetes restart the pod would retry too, but fast and in lockstep
// across pods. Supabase's pooler blocks a user for a while after repeated failed
// logins (ECIRCUITBREAKER), which turned a few seconds of "password not set yet"
// into minutes of crash loops on the first deploy. The jitter spreads the pods
// apart so they stay under that limit.
//
// See D47.
package retry
