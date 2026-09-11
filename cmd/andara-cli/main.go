// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"os"

	"github.com/valesordev/andara/admin/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
