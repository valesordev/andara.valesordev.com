package gateway

import (
	"context"
	"errors"
	"strings"
)

// Principal is who a verified token speaks for. AW-SRV-008 decides what a
// credential is and what a Principal can do; until then this carries only
// the subject the token named, so that a log line and an audit record have
// someone to name.
type Principal struct {
	Subject string
}

// TokenVerifier is the authentication seam. AW-SRV-008 replaces the stub;
// nothing else in the gateway changes when it does.
type TokenVerifier interface {
	// Verify returns the Principal a token speaks for, or an error that
	// maps to UNAUTHENTICATED. The token must never be logged or returned.
	Verify(ctx context.Context, token string) (Principal, error)
}

// StubVerifier accepts any non-empty token. It exists so that Session
// establishment and the Admin interceptor have a verifier to call today,
// which keeps the seam from being retrofitted (AW-SRV-005 scope). It is
// not authentication and must not survive AW-SRV-008.
type StubVerifier struct{}

// Verify accepts any non-empty token and names it "stub".
func (StubVerifier) Verify(_ context.Context, token string) (Principal, error) {
	if strings.TrimSpace(token) == "" {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Subject: "stub"}, nil
}

var errNoBearer = errors.New("missing bearer token")

// bearerToken extracts the credential from an Authorization header for the
// RPCs whose request message carries no token field (every Admin method).
func bearerToken(header string) (string, error) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", errNoBearer
	}
	tok := strings.TrimSpace(header[len(prefix):])
	if tok == "" {
		return "", errNoBearer
	}
	return tok, nil
}

type principalKey struct{}

// PrincipalFrom returns the Principal the interceptor chain attached to ctx,
// if any. Game handlers see one only for authenticated RPCs.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
