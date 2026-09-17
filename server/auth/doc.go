// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package auth is who a caller is and what they may do (AW-SRV-008).
//
// It owns the Account index rebuilt from andara.accounts.v1 at boot, the
// Argon2id credentials on it, the signed stateless session token that
// OpenSession and the Admin bearer header carry, the opaque refresh token
// behind it, the Registration Mode gate, Invite Codes, the audit record every
// privileged action writes, and the authorize decision the command pipeline
// asks before a Command reaches the log.
//
// Two rules shape everything here. Account state is not World state
// (ADR-0006): nothing in this package is reachable from server/sim, and no
// credential rides in a Snapshot. And credential material never appears in a
// log line, a metric label, a span attribute, or an error message — a test
// plants every secret it can and scans for them.
package auth
