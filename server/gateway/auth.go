// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"strings"

	"github.com/valesordev/andara/server/auth"
)

// Principal is who a verified token speaks for. AW-SRV-008 defines it; the
// alias keeps the Gateway's surface stable for the seams that took the
// AW-SRV-005 form.
type Principal = auth.Principal

// TokenVerifier is the authentication seam. auth.Store implements it; the
// Gateway never sees a credential, only the Principal it speaks for.
type TokenVerifier interface {
	// Verify returns the Principal a token speaks for, or an error that
	// maps to UNAUTHENTICATED. The token must never be logged or returned.
	Verify(ctx context.Context, token string) (Principal, error)
	// ActAs returns the Principal for a Session opened as target, or an
	// error that maps to PERMISSION_DENIED (AW-SRV-008 AC-10).
	ActAs(ctx context.Context, p Principal, target string) (Principal, error)
}

// Rechecker re-reads the Account state behind an open Session's Principal.
// A non-nil error closes the Session (AW-SRV-008 AC-12).
type Rechecker interface {
	Recheck(p Principal) error
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

// PrincipalFrom returns the Principal the interceptor chain attached to ctx,
// if any. Admin handlers see one for every call that reached them.
func PrincipalFrom(ctx context.Context) (Principal, bool) { return auth.PrincipalFrom(ctx) }

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return auth.WithPrincipal(ctx, p)
}
