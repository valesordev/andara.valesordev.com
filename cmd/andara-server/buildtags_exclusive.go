// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build fault && sizing

package main

// A fault build and a sizing build are mutually exclusive (AW-SRV-065 AC-7):
// no measurement wants a wedging loop, and no wedge test wants 25,000
// Entities. Go has no #error, so this file fails the build with a message
// that says so.
var _ = build_tags_fault_and_sizing_are_mutually_exclusive
