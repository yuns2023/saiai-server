package tlsfingerprint

// DefaultProfile returns a fresh built-in Claude model-request profile.
// Administrators control enablement, not the contents of the handshake.
func DefaultProfile() *Profile {
	return &Profile{Name: "Claude Code native Linux model requests"}
}
