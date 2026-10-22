// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

// SetInClaim installs the hook claimForced runs between taking the in-flight
// slot and clearing the Force request.
func (s *Snapshotter) SetInClaim(f func()) { s.inClaim = f }
