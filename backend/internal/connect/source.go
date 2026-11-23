package connect

import (
	"regexp"
	"strings"
)

// SourcePrefix starts the reserved source name of a direct connection's records: ryft-direct-<provider>. A sync
// record whose source starts with it is dropped (ingest's screen), so no Apple Health source can be mistaken for
// a connection or overwrite its records.
const SourcePrefix = "ryft-direct-"

// ReservedProvider is the id the callback's redirect uses for a path that names no well-formed provider
// (ryft://connect/unknown, API.md → Connections). It is well-formed, but never a provider: no registry takes it.
const ReservedProvider = "unknown"

var providerRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// ValidProvider says whether id is a well-formed provider id: [a-z][a-z0-9]{1,31}.
func ValidProvider(id string) bool { return providerRe.MatchString(id) }

// SourceName is the source of provider's records.
func SourceName(provider string) string { return SourcePrefix + provider }

// IsReservedSource says whether a record's source name is in the reserved space.
func IsReservedSource(name string) bool { return strings.HasPrefix(name, SourcePrefix) }

// ProviderOfSource is the provider of a reserved source name; false for any other.
func ProviderOfSource(name string) (string, bool) {
	p, ok := strings.CutPrefix(name, SourcePrefix)
	return p, ok && ValidProvider(p)
}

// RecordKey is what a direct row's record_id holds: "<kind>:<record id>" of the inbox row that wrote it.
func RecordKey(kind, recordID string) string { return kind + ":" + recordID }

// vendorsOfBundle maps a HealthKit source's bundle id to the vendor a direct connection of the same maker
// replaces ("direct beats Apple Health for the same vendor", PR 3). Only what is known is listed.
var vendorsOfBundle = map[string]string{
	"com.garmin.connect.mobile": "garmin",
}

// VendorOfBundle is the vendor of a HealthKit record's source_bundle_id; "" when unknown or absent.
func VendorOfBundle(bundleID string) string { return vendorsOfBundle[bundleID] }
