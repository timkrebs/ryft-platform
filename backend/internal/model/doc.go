// Package model owns the wire types of docs/API.md, the event payloads the services exchange, and the error code constants.
//
// It is the shared vocabulary of every service and is mirrored by the iOS app
// (../app/Ryft/Networking/Models.swift), so it imports nothing internal and does
// no I/O. Server-only fields carry json:"-".
//
// Invariants:
//   - Changes are additive: a v1 field is never renamed or removed, and a closed
//     enum (the Known* sets, the Code* constants) says so in API.md.
//   - A struct change shows in testdata/schemas, and every JSON example in
//     API.md is decoded into its type, so the contract and the code cannot
//     drift apart unseen.
//
// See docs/API.md; docs/DECISIONS.md D21 (the Swift check, make check-swift).
package model
