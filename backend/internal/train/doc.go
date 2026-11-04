// Package train holds the pure rules of the Train feature that are not
// scoring: fixed ids for built-in content, slugs, an exercise's name per
// language, request validation, and the session merge and conflict rule.
//
// It must not do I/O. The store persists what these rules accept and the api
// maps their errors to the contract's status codes. The ids are part of the
// contract and never change.
//
// See docs/API.md → Train; D61, D124–D141.
package train
