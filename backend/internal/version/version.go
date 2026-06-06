// Package version exposes the build version of the gateway binary.
//
// The value is overridable at build time via:
//
//	-ldflags "-X github.com/lexbryan/ai.it-dab.com/backend/internal/version.Version=<v>"
//
// (see the backend Makefile), and defaults to a development sentinel otherwise.
package version

// Version is the gateway build version. Release builds override it via ldflags.
var Version = "0.0.0-dev"

// String returns the current build version. It is always non-empty.
func String() string {
	return Version
}
