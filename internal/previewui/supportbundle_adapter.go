package previewui

import (
	"context"

	"github.com/LE-saber/Local-Probe/internal/supportbundle"
)

// BuildSupportBundle is the single Preview export construction seam.  It
// delegates marshaling, the fixed-schema second pass, the 64 KiB limit and
// sensitive/path checks to supportbundle.  Keeping this adapter separate
// avoids an import cycle while allowing the native window to remain unaware
// of supportbundle's implementation details.
func BuildSupportBundle(snapshot Snapshot) ([]byte, error) {
	snapshot = sanitizeSnapshot(snapshot)
	snapshot.SchemaVersion = SchemaVersion
	snapshot.Omitted = appendUniqueAtoms(snapshot.Omitted,
		"raw_config", "raw_audit_lines", "credentials", "network_endpoints", "process_details")
	return supportbundle.GenerateDocument(snapshot)
}

// WriteSupportBundle is the single Preview export persistence seam.  The
// supportbundle package validates data again and performs an atomic,
// no-overwrite write with restrictive permissions.
func WriteSupportBundle(path string, data []byte) error {
	return supportbundle.WriteDocument(path, data)
}

// ExportSupportBundle checks cancellation before building a pure local
// document.  It is useful to adapters that need an explicit context-aware
// export operation without allowing context or I/O concerns into the fixed
// supportbundle API.
func ExportSupportBundle(ctx context.Context, snapshot Snapshot) ([]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return BuildSupportBundle(snapshot)
}

func appendUniqueAtoms(values []string, extras ...string) []string {
	result := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(result)+len(extras))
	for _, value := range result {
		seen[value] = struct{}{}
	}
	for _, value := range extras {
		if len(result) >= MaxItems {
			break
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
