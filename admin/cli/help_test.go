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
