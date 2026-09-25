// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "update --help golden files")

func TestHelpGoldens(t *testing.T) {
	cases := []struct {
		file string
		args []string
	}{
		{file: "root.txt", args: []string{"--help"}},
		{file: "version.txt", args: []string{"version", "--help"}},
		{file: "config.txt", args: []string{"config", "--help"}},
		{file: "config-show.txt", args: []string{"config", "show", "--help"}},
		{file: "completion.txt", args: []string{"completion", "--help"}},
		{file: "sim.txt", args: []string{"sim", "--help"}},
		{file: "sim-repl.txt", args: []string{"sim", "repl", "--help"}},
		{file: "play.txt", args: []string{"play", "--help"}},
		{file: "character.txt", args: []string{"character", "--help"}},
		{file: "character-create.txt", args: []string{"character", "create", "--help"}},
		{file: "character-list.txt", args: []string{"character", "list", "--help"}},
		{file: "content.txt", args: []string{"content", "--help"}},
		{file: "content-compile.txt", args: []string{"content", "compile", "--help"}},
		{file: "content-fmt.txt", args: []string{"content", "fmt", "--help"}},
		{file: "content-decompile.txt", args: []string{"content", "decompile", "--help"}},
		{file: "content-fetch-core.txt", args: []string{"content", "fetch-core", "--help"}},
	}

	dir := filepath.Join("testdata", "help")
	update := *updateGoldens || os.Getenv("UPDATE_GOLDENS") == "1"

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			res := runCLI(t, tc.args, nil)
			if res.exit != ExitOK {
				t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
			}
			path := filepath.Join(dir, tc.file)
			if update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(res.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run `make goldens`)", path, err)
			}
			if string(want) != res.stdout {
				t.Errorf("help output mismatch for %s\nwant:\n%s\ngot:\n%s", tc.file, want, res.stdout)
			}
		})
	}
}
