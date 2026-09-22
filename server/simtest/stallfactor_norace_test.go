// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !race

package simtest_test

// stallFactor scales AC-1's wall-clock assertion for the build it runs in.
//
// Two, here: the copy measures 2.7 ms against a 5 ms budget uncontended and
// 4.8 ms with the machine busy, and CI hardware is slower than the workstation
// those came from. See stallfactor_race_test.go for why this is not one number.
const stallFactor = 2
