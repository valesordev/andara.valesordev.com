// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package sim is the transport-agnostic, dependency-free simulation core.
//
// It imports no network, datastore, filesystem, wall clock, or global
// randomness. Loading, Kafka, and telemetry live in callers.
package sim
