package probe

// fileIdentity is private by design.  Only AuditExecutable can create a
// descriptor carrying a valid identity, so remote input cannot manufacture an
// executable descriptor by supplying JSON-shaped fields.
type fileIdentity struct {
	key   string
	valid bool
}

func sameIdentity(a, b fileIdentity) bool {
	return a.valid && b.valid && a.key != "" && a.key == b.key
}
