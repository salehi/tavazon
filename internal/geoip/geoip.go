// Package geoip wraps the MaxMind reader and builds an ASN-to-prefixes index.
// See docs/project.md §6.5, §7.4. The real implementation lands in Phase 1.
package geoip

import maxminddb "github.com/oschwald/maxminddb-golang"

// Placeholder: keeps the maxminddb dependency wired into the module from
// Phase 0 so it is vendored. The Phase 1 implementation replaces this.
var _ = maxminddb.Open
