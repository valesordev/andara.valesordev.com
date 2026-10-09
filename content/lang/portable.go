// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import "strings"

// PortabilityFault says why a blob path element can't be written on some
// supported platform, or returns "" when it can (AW-CLI-010). It is the one
// rule behind both the compiler's unportable_name and the publish gate's
// refusal (server/content.UnsafeBlobPath, #322), so a name the compiler
// accepts is a name the gate accepts.
//
// An element is faulty when it holds a backslash, a colon or a NUL byte, or
// when it names a Windows device: CON, PRN, AUX, NUL, CONIN$, CONOUT$,
// COM1-9 and LPT1-9 (with ¹, ² and ³ as digits), in any case, ignoring trailing
// spaces and anything after the first ".". The extension rule is conservative:
// "con.aw" is refused, as Windows 10 reserves it, though Windows 11 doesn't.
func PortabilityFault(el string) string {
	switch {
	case strings.ContainsRune(el, '\\'):
		return "contains '\\', which Windows reads as a path separator"
	case strings.ContainsRune(el, ':'):
		return "contains ':', which Windows can't write"
	case strings.ContainsRune(el, 0):
		return "contains a NUL byte, which no platform can write"
	case windowsDevice(el):
		return "is a Windows device name"
	}
	return ""
}

// UnportableElement reports whether PortabilityFault finds anything in el.
func UnportableElement(el string) bool { return PortabilityFault(el) != "" }

func windowsDevice(el string) bool {
	if i := strings.IndexByte(el, '.'); i >= 0 {
		el = el[:i]
	}
	el = strings.ToUpper(strings.TrimRight(el, " "))
	switch el {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if rest, ok := strings.CutPrefix(el, "COM"); ok {
		return isDeviceDigit(rest)
	}
	if rest, ok := strings.CutPrefix(el, "LPT"); ok {
		return isDeviceDigit(rest)
	}
	return false
}

func isDeviceDigit(s string) bool {
	return len(s) == 1 && '1' <= s[0] && s[0] <= '9' || s == "¹" || s == "²" || s == "³"
}

// checkPortable reports each declaration or file whose blob path some
// supported platform can't write (AW-CLI-010), in the three places the
// compiler builds a path from: a Zone ID, a pack name that declares a
// Template, and a source file's path under src/. A Zone's finding carries the
// Zone as its chain, as every Zone-scoped finding does; the others carry none.
func (r *resolver) checkPortable() {
	var packDecl *PackDecl
	var packFile string
	templates := false
	for _, f := range r.files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *PackDecl:
				if packDecl == nil {
					packDecl, packFile = d, f.Path
				}
			case *TemplateDecl:
				templates = true
			case *ZoneDecl:
				el := d.ID + ".json"
				if why := PortabilityFault(el); why != "" {
					r.report(f.Path, d.IDPos, CodeUnportableName,
						unportableMessage(d.ID, el, why), d.ID)
				}
			}
		}
	}
	if templates && packDecl != nil {
		// The Template blobs are templates/<pack>.<Name>.json, so the element
		// is the pack's name plus whatever follows it: one finding per pack,
		// since the pack name is the one thing to rename.
		if why := PortabilityFault(packDecl.Name + ".<Name>.json"); why != "" {
			r.report(packFile, packDecl.NamePos, CodeUnportableName,
				unportableMessage(packDecl.Name, "templates/"+packDecl.Name+".<Name>.json", why))
		}
	}
	for _, f := range r.files {
		for _, el := range strings.Split(SourcePrefix+f.Path, "/") {
			if why := PortabilityFault(el); why != "" {
				r.report(f.Path, Pos{Line: 1, Col: 1}, CodeUnportableName,
					unportableMessage(el, SourcePrefix+f.Path, why))
				break
			}
		}
	}
}

// unportableMessage names the offending name and the rule it breaks. A device
// name also names the blob path it would make, since that is what the Builder
// would meet at publish.
func unportableMessage(name, blobPath, why string) string {
	if strings.HasSuffix(why, "Windows device name") {
		return name + " " + why + ", so " + blobPath + " can't be published; rename it"
	}
	return name + " " + why + "; rename it"
}
