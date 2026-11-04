// Package model owns the wire types of docs/API.md, the event payloads the services exchange, and the error code constants.
//
// It is the shared vocabulary of every service and is mirrored by the iOS app
// (../app/Ryft/Networking/Models.swift; D2: the app lives in ../app/Ryft/), so it imports nothing internal and does
// no I/O. Server-only fields carry json:"-".
//
// Invariants:
//   - Changes are additive: a v1 field is never renamed or removed. An enum is
//     closed only where API.md says so (for example the Code* constants, state,
//     severity, the Train enums, the journal's kind and category), because the
//     app decodes those as Swift enums. The other Known* sets are the values the
//     server accepts or stores on the request side, and some grow additively.
//   - A struct change shows in testdata/schemas, and every JSON example in
//     API.md is decoded into its type or listed with a reason, so the contract
//     and the code cannot drift apart unseen.
//
// See docs/API.md; docs/DECISIONS.md D21 (the Swift check, make check-swift).
package model
