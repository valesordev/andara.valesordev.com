// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unportableZone is AC-8's corpus case, held here until architecture lands
// the case directory at §8: the sources and the sidecar are the story's.
const unportableZone = `zone aux "Zone" {
  fallback hall

  room hall "The Hall" {
    exit north -> yard
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
`

func portablePack(t *testing.T, files map[string]string) (string, []Diagnostic) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, p, body)
	}
	_, ds := Compile(dir, corpusCore(t), nil)
	return dir, ds
}

func codes(ds []Diagnostic, code string) []Diagnostic {
	var out []Diagnostic
	for _, d := range ds {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

// TestUnportableName_AZoneIDThatIsADeviceName is AC-1, with the corpus case's
// sidecar asserted against the compiler's output.
func TestUnportableName_AZoneIDThatIsADeviceName(t *testing.T) {
	_, ds := portablePack(t, map[string]string{
		"pack.aw": "pack p requires andara.core@1\n",
		"z.aw":    unportableZone,
	})
	if len(ds) != 1 {
		t.Fatalf("want one diagnostic, got %v", ds)
	}
	d := ds[0]
	if d.Code != CodeUnportableName || d.File != "z.aw" || d.Line != 1 || d.Col != 6 {
		t.Errorf("want z.aw:1:6 unportable_name, got %s", d)
	}
	for _, want := range []string{"aux", "Windows device name", "aux.json"} {
		if !strings.Contains(d.Message, want) {
			t.Errorf("the message %q omits %q", d.Message, want)
		}
	}
	var sb strings.Builder
	d.Render(&sb)
	if want := "z.aw:1:6: unportable_name " + d.Message + "\n  aux\n"; sb.String() != want {
		t.Errorf("rendered %q, want %q", sb.String(), want)
	}
	if got := sidecar(ds); got != "z.aw:1:6: unportable_name\n  aux\n" {
		t.Errorf("the sidecar form is %q", got)
	}
}

// sidecar renders diagnostics the way a corpus expected.errors holds them:
// position and code, then the chain.
func sidecar(ds []Diagnostic) string {
	var sb strings.Builder
	for _, d := range ds {
		sb.WriteString(d.File + ":" + itoa(d.Line) + ":" + itoa(d.Col) + ": " + d.Code + "\n")
		for _, c := range d.Chain {
			sb.WriteString("  " + c + "\n")
		}
	}
	return sb.String()
}

// TestUnportableName_APackThatDeclaresTemplatesIsReportedOncePerPack is AC-2.
func TestUnportableName_APackThatDeclaresTemplatesIsReportedOncePerPack(t *testing.T) {
	_, ds := portablePack(t, map[string]string{
		"pack.aw": "pack con requires andara.core@1\n",
		"t.aw":    "template One kind item {}\ntemplate Two kind item {}\ntemplate Three kind item {}\n",
	})
	got := codes(ds, CodeUnportableName)
	if len(got) != 1 {
		t.Fatalf("want one finding for the pack, got %v", ds)
	}
	if got[0].File != "pack.aw" || got[0].Line != 1 || got[0].Col != 6 || len(got[0].Chain) != 0 {
		t.Errorf("want pack.aw:1:6 with no chain, got %s chain %v", got[0], got[0].Chain)
	}
}

func TestUnportableName_APackWithNoTemplateIsNotReported(t *testing.T) {
	_, ds := portablePack(t, map[string]string{
		"pack.aw": "pack con requires andara.core@1\n",
		"z.aw":    "zone z \"Z\" {\n  fallback r\n\n  room r \"R\" {}\n}\n",
	})
	if got := codes(ds, CodeUnportableName); len(got) != 0 {
		t.Errorf("a pack named con with no Template makes no device-named blob: %v", got)
	}
}

// TestUnportableName_ASourcePathIsReportedPerFile is AC-3 and AC-6.
func TestUnportableName_ASourcePathIsReportedPerFile(t *testing.T) {
	_, ds := portablePack(t, map[string]string{
		"pack.aw":        "pack p requires andara.core@1\n",
		"zones/nul.aw":   "",
		"lpt1/a.aw":      "",
		"lpt1/b.aw":      "",
		"con/nul.aw":     "",
		"zones/a:b.aw":   "",
		"zones/fine.aw":  "",
		"console/ok.aw":  "",
		"auxiliary/x.aw": "",
	})
	var got []string
	for _, d := range codes(ds, CodeUnportableName) {
		if d.Line != 1 || d.Col != 1 || len(d.Chain) != 0 {
			t.Errorf("%s: want 1:1 and no chain, got %d:%d chain %v", d.File, d.Line, d.Col, d.Chain)
		}
		got = append(got, d.File)
	}
	want := "con/nul.aw lpt1/a.aw lpt1/b.aw zones/a:b.aw zones/nul.aw" // one per file, though con/nul.aw has two bad elements
	if strings.Join(got, " ") != want {
		t.Errorf("reported files %v, want %s", got, want)
	}
	for _, d := range codes(ds, CodeUnportableName) {
		switch d.File {
		case "zones/a:b.aw":
			if !strings.Contains(d.Message, "a:b.aw") || !strings.Contains(d.Message, "':'") {
				t.Errorf("the colon message %q must name the element and the rule", d.Message)
			}
		case "lpt1/a.aw":
			if !strings.Contains(d.Message, "lpt1") {
				t.Errorf("the message %q must name the directory element", d.Message)
			}
		}
	}
}

// TestUnportableName_OnlyTheDeviceNamesThemselves is AC-4 and AC-5.
func TestUnportableName_OnlyTheDeviceNamesThemselves(t *testing.T) {
	for name, want := range map[string]bool{
		"console": false, "auxiliary": false, "com10": false, "lpt0": false, "com0": false, "nullable": false,
		"Aux": true, "COM¹": true, "COM²": true, "lpt³": true, "con.aw": true, "nul.v2": true, "aux .aw": true, "conin$": true,
		"a:b": true, `a\b`: true, "a\x00b": true,
		"fine.aw": false, "..aw": false,
	} {
		if got := UnportableElement(name); got != want {
			t.Errorf("UnportableElement(%q) = %v, want %v", name, got, want)
		}
	}
}
