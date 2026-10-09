// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/valesordev/andara/content/lang"
)

// TestTheCompilerAndTheGateAgreeOnWhatIsUnportable is AW-CLI-010 AC-7: for
// every form the gate refuses in a path element, the compiler's verdict on a
// source file of that name is the gate's verdict on its blob path. The two call
// one predicate (lang.UnportableElement); this holds them to it from outside,
// so a second copy of the rule in either place fails here.
func TestTheCompilerAndTheGateAgreeOnWhatIsUnportable(t *testing.T) {
	names := []string{
		// each device name, and its variants
		"con", "CON", "Con", "prn", "aux", "nul", "NUL", "conin$", "CONOUT$",
		"com1", "COM9", "lpt1", "LPT9", "com¹", "COM²", "lpt³",
		"con.v2", "nul.x.y", "aux ", "aux  ",
		// the forms that merely contain one
		"console", "auxiliary", "com10", "com0", "lpt0", "lpt", "nullable", "conx", "xcon", "con-1",
		// the other forms the gate refuses, and ones it doesn't
		"a:b", ":", "a:", `a\b`, `\`, "a\x00b", "\x00",
		"fine", "..a", "a..b", "x.y.z", "dollar$",
	}
	for _, name := range names {
		el := name + ".aw"
		gate := UnsafeBlobPath(lang.SourcePrefix + "zones/" + el)
		if got := lang.UnportableElement(el); got != gate {
			t.Errorf("%q: UnportableElement = %v, the gate says %v", el, got, gate)
		}
		if name == "\x00" || name == "a\x00b" {
			continue // no filesystem holds a NUL in a file name; the predicate check above is the verdict
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "zones"), 0o755); err != nil {
			t.Fatal(err)
		}
		for rel, body := range map[string]string{"pack.aw": "pack p\n", "zones/" + el: ""} {
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
				t.Fatalf("writing %q: %v", rel, err)
			}
		}
		_, ds := lang.Compile(dir, nil, nil)
		compiler := false
		for _, d := range ds {
			if d.Code == lang.CodeUnportableName {
				compiler = true
			}
		}
		if compiler != gate {
			t.Errorf("%q: the compiler says unportable=%v, the gate says %v (diagnostics %v)", el, compiler, gate, ds)
		}
	}
}
