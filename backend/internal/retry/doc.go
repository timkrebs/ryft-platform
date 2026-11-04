// Package retry owns start-up retry with backoff and jitter for dependencies that
// may not be up yet.
//
// Those are the database before the migrate job has set the role passwords, and
// RabbitMQ before its nodes are ready. It must not wrap a request-path call; it is for start-up only. Exiting and
// letting Kubernetes restart the pod would retry too, but fast and in lockstep
// across pods. Supabase's pooler blocks a user for a while after repeated failed
// logins (ECIRCUITBREAKER), which turned a few seconds of "password not set yet"
// into minutes of crash loops on the first deploy. The backoff keeps the number of
// failed logins down (D93, D47); the jitter only spreads the pods apart.
//
// See D47.
package retry
