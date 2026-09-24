// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !race

package lang_test

// compileFactor scales AC-7's wall-clock assertion for the build it runs in.
// One, here: the budget is the story's own 5 s and the compile measures far
// under it. See sizing_race_test.go for why this is not one number.
const compileFactor = 1
