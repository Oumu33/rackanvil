// Package version carries build identity injected at release time via
// -ldflags -X (see scripts/release.sh). Defaults mark a dev build so a
// hand-built binary never pretends to be a release.
package version

import "fmt"

// Build identity. Overridden by scripts/release.sh:
//
//	-X github.com/Oumu33/rackanvil/internal/version.Version=1.0.0
//	-X github.com/Oumu33/rackanvil/internal/version.Commit=<git short hash or "nogit">
//	-X github.com/Oumu33/rackanvil/internal/version.BuildDate=<UTC RFC3339>
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// String renders a one-line identity used by both binaries' -version flag.
func String() string {
	return fmt.Sprintf("rackanvil %s (commit %s, built %s)", Version, Commit, BuildDate)
}
