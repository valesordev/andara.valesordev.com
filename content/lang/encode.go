// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// CanonicalJSON renders a message as the blob format the loader reads
// (semantics.md §7):
//
//  1. formatVersion first, then the remaining keys in field-number order
//  2. two-space indent, one key or element per line, LF, a trailing LF
//  3. fields at their default omitted; enums as their names
//  4. every repeated field sorted — done when the message is built, because
//     `chain` is in chain order and is not a sort
//
// It walks protoreflect rather than calling protojson, for the reason
// semantics.md §7 names: Go's protojson deliberately injects non-deterministic
// whitespace, so "byte for byte" through it would be a claim about a
// serializer rather than about content.
//
// The escaping matches `json.dumps(obj, indent=2, ensure_ascii=False)`, which
// is what wrote every expected blob in the corpus and what
// scripts/content_grammar_check.py asserts them against. Go's encoding/json
// differs in both directions — it escapes `<`, `>` and `&`, and it does not
// escape what Python does — so neither its Marshal nor its Indent is usable
// here.
func CanonicalJSON(m protoreflect.ProtoMessage) []byte {
	var sb strings.Builder
	writeMessage(&sb, m.ProtoReflect(), 0)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

// formatVersionField is hoisted ahead of field-number order. Version first is
// how a versioned document is read, and it is the rule that reproduces every
// fixture on disk: TemplateDefinition.format_version is field 8 and every seed
// file puts it first.
const formatVersionField = "format_version"

func writeMessage(sb *strings.Builder, m protoreflect.Message, depth int) {
	type entry struct {
		fd protoreflect.FieldDescriptor
		v  protoreflect.Value
	}
	var fields []entry
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		fields = append(fields, entry{fd, v})
		return true
	})
	if len(fields) == 0 {
		sb.WriteString("{}")
		return
	}
	sort.Slice(fields, func(i, j int) bool {
		a, b := fields[i].fd, fields[j].fd
		if av, bv := a.Name() == formatVersionField, b.Name() == formatVersionField; av != bv {
			return av
		}
		return a.Number() < b.Number()
	})

	pad := strings.Repeat("  ", depth+1)
	sb.WriteString("{\n")
	for i, f := range fields {
		if i > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString(pad)
		writeJSONString(sb, f.fd.JSONName())
		sb.WriteString(": ")
		writeValue(sb, f.fd, f.v, depth+1)
	}
	sb.WriteByte('\n')
	sb.WriteString(strings.Repeat("  ", depth))
	sb.WriteByte('}')
}

func writeValue(sb *strings.Builder, fd protoreflect.FieldDescriptor, v protoreflect.Value, depth int) {
	if fd.IsList() {
		list := v.List()
		if list.Len() == 0 {
			sb.WriteString("[]")
			return
		}
		pad := strings.Repeat("  ", depth+1)
		sb.WriteString("[\n")
		for i := 0; i < list.Len(); i++ {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(pad)
			writeScalar(sb, fd, list.Get(i), depth+1)
		}
		sb.WriteByte('\n')
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteByte(']')
		return
	}
	writeScalar(sb, fd, v, depth)
}

func writeScalar(sb *strings.Builder, fd protoreflect.FieldDescriptor, v protoreflect.Value, depth int) {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		writeMessage(sb, v.Message(), depth)
	case protoreflect.EnumKind:
		// Enums as their names ("ENTITY"), which is protojson's own rule and
		// what every fixture on disk holds.
		ev := fd.Enum().Values().ByNumber(v.Enum())
		if ev == nil {
			sb.WriteString(strconv.FormatInt(int64(v.Enum()), 10))
			return
		}
		writeJSONString(sb, string(ev.Name()))
	case protoreflect.StringKind:
		writeJSONString(sb, v.String())
	case protoreflect.BoolKind:
		sb.WriteString(strconv.FormatBool(v.Bool()))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		// 64-bit integers are JSON strings in protobuf JSON, because a JSON
		// number cannot hold the full int64 range exactly.
		writeJSONString(sb, strconv.FormatInt(v.Int(), 10))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		writeJSONString(sb, strconv.FormatUint(v.Uint(), 10))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		sb.WriteString(strconv.FormatInt(v.Int(), 10))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		sb.WriteString(strconv.FormatUint(v.Uint(), 10))
	default:
		sb.WriteString(fmt.Sprintf("%v", v.Interface()))
	}
}

// writeJSONString escapes exactly as Python's json.dumps with
// ensure_ascii=False does: the two mandatory escapes, the five short forms,
// \u00xx for the remaining C0 controls, and every other rune written through
// as UTF-8. Notably it does *not* escape `<`, `>`, `&`, U+2028 or U+2029 —
// all of which Go's encoding/json escapes, and none of which the corpus's
// expected bytes carry escaped.
func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 {
				sb.WriteString(fmt.Sprintf(`\u%04x`, r))
				continue
			}
			if r == utf8.RuneError {
				// A byte sequence that is not UTF-8 reached the encoder;
				// encoding is refused before this point, so this is a
				// defensive spelling rather than a path content takes.
				sb.WriteString(`�`)
				continue
			}
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}
