// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"sort"
	"strconv"
	"strings"
)

// CanonicalBytes serializes World topology in a stable order: zones by ID,
// rooms by ID, exits by Direction. Two loads of the same content must compare
// equal (AW-SRV-001 AC-10).
//
// Free-text fields are escaped, so the encoding is injective: distinct
// topologies never produce the same bytes. Without escaping, a Room
// description containing a tab or a newline can impersonate a field boundary
// or a whole record, which would make a comparison of two serializations prove
// less than it appears to. Nothing persists this output today; ADR-0002's State
// Hash is a separate artifact. It is canonical anyway, because a serializer
// that content can forge is the wrong thing to build a determinism check on.
//
// Components are emitted as their own records, one per Component and one per
// field, so content that carries none produces byte-identical output to what it
// produced before Components existed (AW-SRV-021 AC-8). A field record carries
// its kind tag, which is what keeps the encoding injective across the oneof: the
// string "1" and the integer 1 must not serialize the same.
func CanonicalBytes(w *World) []byte {
	if w == nil {
		return nil
	}
	var b strings.Builder
	zids := make([]string, 0, len(w.Zones))
	for id := range w.Zones {
		zids = append(zids, string(id))
	}
	sort.Strings(zids)
	for _, zid := range zids {
		z := w.Zones[ZoneID(zid)]
		writeFields(&b, "zone", zid, z.Name, strconv.FormatInt(int64(z.Partition), 10))
		writeComponents(&b, "zone_component", "zone_field", []string{zid}, z.Components)

		rids := make([]string, 0, len(z.Rooms))
		for id := range z.Rooms {
			rids = append(rids, string(id))
		}
		sort.Strings(rids)
		for _, rid := range rids {
			r := z.Rooms[RoomID(rid)]
			writeFields(&b, "room", zid, rid, r.Title, r.Description)
			writeComponents(&b, "room_component", "room_field", []string{zid, rid}, r.Components)
			for _, e := range r.Exits {
				crossing := "local"
				if e.CrossZone {
					crossing = "cross"
				}
				writeFields(&b, "exit", zid, rid,
					string(e.Direction), string(e.To.Zone), string(e.To.Room), crossing)
			}
		}
	}
	return []byte(b.String())
}

// EntityCanonicalBytes serializes one Entity the way CanonicalBytes
// serializes topology: injective, sorted, tagged. It is how AW-SRV-022 AC-5
// is asserted — two Instantiate calls with the same inputs encode
// byte-identically — and, like CanonicalBytes, it is not the State Hash.
func EntityCanonicalBytes(e EntityState) []byte {
	var b strings.Builder
	writeFields(&b, "entity", string(e.ID), string(e.Template), e.ContentVersion)
	writeComponents(&b, "entity_component", "entity_field", []string{string(e.ID)}, e.Components)
	return []byte(b.String())
}

// writeComponents emits a record per Component and a record per field beneath
// it. Both are already sorted — by type at load, by name at load — and are
// sorted again here rather than trusted, because a World assembled by hand in a
// test is not obliged to have gone through the loader, and a serializer that
// silently depends on its input being sorted is a determinism bug waiting for
// the first caller who does not know that.
func writeComponents(b *strings.Builder, compRec, fieldRec string, scope []string, comps []Component) {
	if len(comps) == 0 {
		return
	}
	ordered := make([]Component, len(comps))
	copy(ordered, comps)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Type < ordered[j].Type })
	for _, c := range ordered {
		head := append([]string{compRec}, scope...)
		writeFields(b, append(head, string(c.Type))...)
		fields := make([]ComponentField, len(c.Fields))
		copy(fields, c.Fields)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, f := range fields {
			rec := append([]string{fieldRec}, scope...)
			rec = append(rec, string(c.Type), f.Name, f.Kind.String(), f.Value())
			writeFields(b, rec...)
		}
	}
}

// writeFields emits one tab-separated, newline-terminated record with every
// field escaped, so no field value can introduce a separator.
func writeFields(b *strings.Builder, fields ...string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		writeEscaped(b, f)
	}
	b.WriteByte('\n')
}

// writeEscaped escapes the two separators and the escape character itself.
// Reversible, which is what makes the encoding injective.
func writeEscaped(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(c)
		}
	}
}
