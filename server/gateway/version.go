package gateway

// Negotiate applies the Protocol version rule from ADR-0003: the server
// declares a supported range; a client inside it gets the version it asked
// for; a client outside it is rejected with a typed error naming both. There
// is no fallback and no "closest supported" — silently degrading a client is
// the failure mode this integer exists to prevent.
//
// The client declares one version rather than a range, so there is nothing
// to choose: negotiated is client. The response still carries the server's
// range so the client can log what else would have worked.
func Negotiate(client, minVersion, maxVersion uint32) (uint32, error) {
	if client < minVersion || client > maxVersion {
		return 0, &VersionError{Client: client, Min: minVersion, Max: maxVersion}
	}
	return client, nil
}
