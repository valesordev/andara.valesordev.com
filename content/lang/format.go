// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// ProseWidth is where `desc` wraps: 100 columns, not 80, because room
// descriptions are prose and 80 puts four words on a line (formatting.md §4).
const ProseWidth = 100

// Format rewrites source to the canonical form (formatting.md). Two properties
// hold and AC-3 asserts both: it is idempotent, and it never changes meaning.
//
// It reorders nothing — not Rooms within a Zone, not Exits within a Room, not
// declarations within a file. A Builder who lays out a Zone as the walk through
// it keeps that layout. The canonical *output* is sorted regardless
// (semantics.md §7), so determinism never depended on the source order, and
// gofmt has never reordered a Go file either (formatting.md §1).
func Format(src []byte) ([]byte, []Diagnostic) {
	f, ds := ParseFile("", string(src))
	if len(ds) > 0 {
		return nil, ds
	}
	return []byte(newPrinter(f).file(f)), nil
}

// printer renders an AST. Comments are placed by position: a comment on its own
// line is re-indented to the line it precedes, and a trailing comment keeps its
// line (formatting.md §5).
type printer struct {
	sb       strings.Builder
	comments []Token
	next     int
	canon    bool // decompile: no comments, canonical order, one-line Components
}

func newPrinter(f *File) *printer {
	cs := append([]Token{}, f.Comments...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Pos.Line != cs[j].Pos.Line {
			return cs[i].Pos.Line < cs[j].Pos.Line
		}
		return cs[i].Pos.Col < cs[j].Pos.Col
	})
	return &printer{comments: cs}
}

func (p *printer) file(f *File) string {
	for i, d := range f.Decls {
		if i > 0 {
			// Exactly one blank line between top-level declarations; none
			// before the first or after the last (formatting.md §5).
			p.sb.WriteByte('\n')
		}
		p.ownComments(d.declPos().Line, 0)
		p.decl(d)
	}
	p.trailingOwnComments(0, len(f.Decls) > 0)
	out := p.sb.String()
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	return out + "\n"
}

// ownComments emits every pending comment that sits on its own line above
// `line`, re-indented to depth.
func (p *printer) ownComments(line, depth int) {
	for p.next < len(p.comments) && p.comments[p.next].Pos.Line < line {
		c := p.comments[p.next]
		p.next++
		if p.canon {
			continue
		}
		p.indent(depth)
		p.sb.WriteString(c.Text)
		p.sb.WriteByte('\n')
	}
}

func (p *printer) trailingOwnComments(depth int, blankBefore bool) {
	if p.next >= len(p.comments) || p.canon {
		p.next = len(p.comments)
		return
	}
	if blankBefore {
		p.sb.WriteByte('\n')
	}
	for ; p.next < len(p.comments); p.next++ {
		p.indent(depth)
		p.sb.WriteString(p.comments[p.next].Text)
		p.sb.WriteByte('\n')
	}
}

// trailing emits a comment that shares `line` with the code just written,
// separated by two spaces (formatting.md §5).
func (p *printer) trailing(line int) {
	for p.next < len(p.comments) && p.comments[p.next].Pos.Line == line {
		c := p.comments[p.next]
		p.next++
		if p.canon {
			continue
		}
		p.sb.WriteString("  ")
		p.sb.WriteString(c.Text)
	}
}

func (p *printer) indent(depth int) { p.sb.WriteString(strings.Repeat("  ", depth)) }

func (p *printer) decl(d Decl) {
	switch v := d.(type) {
	case *PackDecl:
		p.pack(v)
	case *ZoneDecl:
		p.zone(v)
	case *TemplateDecl:
		p.template(v)
	}
}

func (p *printer) pack(d *PackDecl) {
	p.sb.WriteString("pack ")
	p.sb.WriteString(d.Name)
	if d.Requires != nil {
		p.sb.WriteString(" requires ")
		p.sb.WriteString(d.Requires.Pack)
		p.sb.WriteByte('@')
		p.sb.WriteString(itoa(int(d.Requires.Version)))
	}
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
}

func (p *printer) zone(d *ZoneDecl) {
	p.sb.WriteString("zone ")
	p.sb.WriteString(d.ID)
	p.sb.WriteByte(' ')
	p.sb.WriteString(quote(d.Name))
	items := d.Items
	if p.canon {
		items = canonicalZoneItems(d)
	}
	if len(items) == 0 {
		p.sb.WriteString(" {}")
		p.trailing(d.Pos.Line)
		p.sb.WriteByte('\n')
		return
	}
	p.sb.WriteString(" {")
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
	for i, it := range items {
		if i > 0 && p.blankBetweenZoneItems(items[i-1], it) {
			p.sb.WriteByte('\n')
		}
		p.ownComments(it.itemPos().Line, 1)
		p.zoneItem(it, 1)
	}
	p.sb.WriteString("}\n")
}

// blankBetweenZoneItems preserves a Builder's blank line between Rooms and
// drops runs of them to one (formatting.md §5). decompile always separates
// Rooms with one blank line, since it has no record of what was written.
func (p *printer) blankBetweenZoneItems(prev, cur ZoneItem) bool {
	if p.canon {
		_, prevRoom := prev.(*RoomDecl)
		_, curRoom := cur.(*RoomDecl)
		return prevRoom || curRoom
	}
	return cur.itemPos().Line > prev.itemPos().Line+1
}

func (p *printer) zoneItem(it ZoneItem, depth int) {
	switch v := it.(type) {
	case *FallbackDecl:
		p.indent(depth)
		p.sb.WriteString("fallback ")
		p.sb.WriteString(v.Room)
		p.trailing(v.Pos.Line)
		p.sb.WriteByte('\n')
	case *ComponentDecl:
		p.component(v, depth)
	case *RoomDecl:
		p.room(v, depth)
	}
}

func (p *printer) room(d *RoomDecl, depth int) {
	p.indent(depth)
	p.sb.WriteString("room ")
	p.sb.WriteString(d.ID)
	p.sb.WriteByte(' ')
	p.sb.WriteString(quote(d.Title))
	items := d.Items
	if p.canon {
		items = canonicalRoomItems(d)
	}
	if len(items) == 0 {
		p.sb.WriteString(" {}")
		p.trailing(d.Pos.Line)
		p.sb.WriteByte('\n')
		return
	}
	p.sb.WriteString(" {")
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
	for i, it := range items {
		if !p.canon && i > 0 && it.roomItemPos().Line > items[i-1].roomItemPos().Line+1 {
			p.sb.WriteByte('\n')
		}
		p.ownComments(it.roomItemPos().Line, depth+1)
		p.roomItem(it, depth+1)
	}
	p.indent(depth)
	p.sb.WriteString("}\n")
}

func (p *printer) roomItem(it RoomItem, depth int) {
	switch v := it.(type) {
	case *DescDecl:
		p.desc(v, depth)
	case *ExitDecl:
		p.exit(v, depth)
	case *ComponentDecl:
		p.component(v, depth)
	}
}

// desc wraps at ProseWidth, breaking between string literals with continuation
// literals aligned under the first. Adjacent literals join with a single space,
// so the break points carry no meaning and fmt may move them freely — the one
// place fmt rewrites a value's layout without touching the value
// (formatting.md §4).
func (p *printer) desc(d *DescDecl, depth int) {
	prefix := strings.Repeat("  ", depth) + "desc "
	cont := strings.Repeat(" ", utf8.RuneCountInString(prefix))
	for i, line := range wrapProse(d.Value, utf8.RuneCountInString(prefix)) {
		if i == 0 {
			p.sb.WriteString(prefix)
		} else {
			p.sb.WriteString(cont)
		}
		p.sb.WriteString(quote(line))
		if i == 0 {
			p.trailing(d.Pos.Line)
		}
		p.sb.WriteByte('\n')
	}
}

// wrapProse breaks the joined text at existing spaces so that each literal,
// once quoted, fits the budget. It never breaks inside a word and never breaks
// a run with no space in it (formatting.md §4).
func wrapProse(s string, indent int) []string {
	budget := ProseWidth - indent - 2 // the two quotes
	if budget < 1 || utf8.RuneCountInString(escape(s)) <= budget {
		return []string{s}
	}
	words := strings.Split(s, " ")
	var out []string
	cur := ""
	for _, w := range words {
		cand := w
		if cur != "" {
			cand = cur + " " + w
		}
		if cur != "" && utf8.RuneCountInString(escape(cand)) > budget {
			out = append(out, cur)
			cur = w
			continue
		}
		cur = cand
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (p *printer) exit(d *ExitDecl, depth int) {
	p.indent(depth)
	p.sb.WriteString("exit ")
	p.sb.WriteString(d.Direction)
	p.sb.WriteString(" -> ")
	p.sb.WriteString(qualify(d.ToZone, d.ToRoom))
	if d.PerceivesSet {
		// The only comma-separated list in the language: `[sight, sound]`,
		// no space inside the brackets and one after each comma
		// (formatting.md §3).
		names := make([]string, len(d.Perceives))
		for i, s := range d.Perceives {
			names[i] = s.Name
		}
		if p.canon {
			sort.Strings(names)
		}
		p.sb.WriteString(" perceives [")
		p.sb.WriteString(strings.Join(names, ", "))
		p.sb.WriteByte(']')
	}
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
}

func (p *printer) template(d *TemplateDecl) {
	p.sb.WriteString("template ")
	p.sb.WriteString(d.Name)
	for _, h := range d.Heads {
		if h.Kind != "" {
			p.sb.WriteString(" kind ")
			p.sb.WriteString(h.Kind)
			continue
		}
		p.sb.WriteString(" extends ")
		p.sb.WriteString(h.Extends)
	}
	items := d.Items
	if p.canon {
		items = canonicalTemplateItems(d)
	}
	if len(items) == 0 {
		p.sb.WriteString(" {}")
		p.trailing(d.Pos.Line)
		p.sb.WriteByte('\n')
		return
	}
	p.sb.WriteString(" {")
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
	for i, it := range items {
		if !p.canon && i > 0 && it.templateItemPos().Line > items[i-1].templateItemPos().Line+1 {
			p.sb.WriteByte('\n')
		}
		p.ownComments(it.templateItemPos().Line, 1)
		p.templateItem(it, 1)
	}
	p.sb.WriteString("}\n")
}

func (p *printer) templateItem(it TemplateItem, depth int) {
	switch v := it.(type) {
	case *ComponentDecl:
		p.component(v, depth)
	case *RemoveDecl:
		p.indent(depth)
		if v.Field == "" {
			p.sb.WriteString("remove component ")
			p.sb.WriteString(v.Component)
		} else {
			p.sb.WriteString("remove field ")
			p.sb.WriteString(v.Field)
			p.sb.WriteString(" from ")
			p.sb.WriteString(v.Component)
		}
		p.trailing(v.Pos.Line)
		p.sb.WriteByte('\n')
	}
}

// component writes `component <type> { … }`. fmt neither collapses a multi-line
// Component that would fit nor expands a one-liner: both forms are canonical,
// and which one a Builder wrote is information — a Component written open is
// usually one they are still working on (formatting.md §3). decompile has no
// such record and always emits the one-liner when it fits.
func (p *printer) component(d *ComponentDecl, depth int) {
	p.indent(depth)
	p.sb.WriteString("component ")
	p.sb.WriteString(d.Type)
	if len(d.Fields) == 0 {
		p.sb.WriteString(" {}")
		p.trailing(d.Pos.Line)
		p.sb.WriteByte('\n')
		return
	}
	if !d.MultiLine && p.oneLineFits(d, depth) {
		p.sb.WriteString(" { ")
		for i, f := range d.Fields {
			if i > 0 {
				p.sb.WriteByte(' ')
			}
			p.sb.WriteString(f.Name)
			p.sb.WriteString(": ")
			p.sb.WriteString(f.Value.canonical())
		}
		p.sb.WriteString(" }")
		p.trailing(d.Pos.Line)
		p.sb.WriteByte('\n')
		return
	}
	p.sb.WriteString(" {")
	p.trailing(d.Pos.Line)
	p.sb.WriteByte('\n')
	for _, f := range d.Fields {
		p.ownComments(f.NamePos.Line, depth+1)
		p.indent(depth + 1)
		p.sb.WriteString(f.Name)
		p.sb.WriteString(": ")
		p.sb.WriteString(f.Value.canonical())
		p.trailing(f.NamePos.Line)
		p.sb.WriteByte('\n')
	}
	p.indent(depth)
	p.sb.WriteString("}\n")
}

func (p *printer) oneLineFits(d *ComponentDecl, depth int) bool {
	n := depth*2 + len("component ") + utf8.RuneCountInString(d.Type) + len(" {  }")
	for i, f := range d.Fields {
		if i > 0 {
			n++
		}
		n += utf8.RuneCountInString(f.Name) + 2 + utf8.RuneCountInString(f.Value.canonical())
	}
	return n <= ProseWidth
}

// canonical renders a value in the one spelling the language has for it.
func (v Value) canonical() string {
	switch v.Kind {
	case String:
		return quote(v.Str)
	case Int, Float, LowerID:
		return v.Raw
	}
	return v.Raw
}

// quote writes a string literal with the language's escape set — \n, \" and
// \\ and nothing else.
func quote(s string) string { return `"` + escape(s) + `"` }

func escape(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '\n':
			sb.WriteString(`\n`)
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
