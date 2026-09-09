package sim

import (
	"sort"
	"strconv"
	"strings"
)

// CanonicalBytes serializes World topology in a stable order: zones by ID,
// rooms by ID, exits by Direction. Two loads of the same content must compare
// equal (AW-SRV-001 AC-10). Does not hash Room field sets — a later component
// field is additive.
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
		b.WriteString("zone\t")
		b.WriteString(zid)
		b.WriteByte('\t')
		b.WriteString(z.Name)
		b.WriteByte('\t')
		b.WriteString(strconv.FormatInt(int64(z.Partition), 10))
		b.WriteByte('\n')

		rids := make([]string, 0, len(z.Rooms))
		for id := range z.Rooms {
			rids = append(rids, string(id))
		}
		sort.Strings(rids)
		for _, rid := range rids {
			r := z.Rooms[RoomID(rid)]
			b.WriteString("room\t")
			b.WriteString(zid)
			b.WriteByte('\t')
			b.WriteString(rid)
			b.WriteByte('\t')
			b.WriteString(r.Title)
			b.WriteByte('\t')
			b.WriteString(r.Description)
			b.WriteByte('\n')
			for _, e := range r.Exits {
				b.WriteString("exit\t")
				b.WriteString(zid)
				b.WriteByte('\t')
				b.WriteString(rid)
				b.WriteByte('\t')
				b.WriteString(string(e.Direction))
				b.WriteByte('\t')
				b.WriteString(string(e.To.Zone))
				b.WriteByte('\t')
				b.WriteString(string(e.To.Room))
				b.WriteByte('\t')
				if e.CrossZone {
					b.WriteString("cross")
				} else {
					b.WriteString("local")
				}
				b.WriteByte('\n')
			}
		}
	}
	return []byte(b.String())
}
