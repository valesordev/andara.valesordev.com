package main

import (
	"os"

	"github.com/valesordev/andara/admin/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
