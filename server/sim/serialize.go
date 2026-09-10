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
// Room field sets are not hashed — a later component field (ADR-0010) is
// additive and must not change the encoding of a Room that has none.
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

		rids := make([]string, 0, len(z.Rooms))
		for id := range z.Rooms {
			rids = append(rids, string(id))
		}
		sort.Strings(rids)
		for _, rid := range rids {
			r := z.Rooms[RoomID(rid)]
			writeFields(&b, "room", zid, rid, r.Title, r.Description)
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
