// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package content loads ZoneDefinition values from a configured source.
//
// The simulation core must not touch the filesystem or Kafka; this package is
// the adapter. Validation stays in sim.BuildWorld.
package content
