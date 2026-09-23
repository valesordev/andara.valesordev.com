// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// parser is a hand-written recursive-descent parser (the story's own
// [ASSUMPTION]). It is contextual by construction, which is what the language
// requires: there are no reserved words, so `entity` is a Template kind in one
// position and a legal Room id in another (semantics.md §1). Every keyword
// below is matched as "a LowerID whose text is X, here", never as a token kind.
//
// A syntax error stops the parse of this file and no other (errors.md rule 4),
// so a file produces at most one syntax_error and the rest of the pack still
// reports.
type parser struct {
	file string
	src  string
	toks []Token // comments filtered out
	i    int
}

// errSyntax carries a syntax error out through the recursive descent. The
// parser reports at the position of the unexpected token, which is the rule
// every case in corpus/invalid/syntax/ was written to.
type errSyntax struct {
	pos  Pos
	want string
	got  Token
}

func (e *errSyntax) Error() string { return "syntax error" }

// message renders what errors.md §3.1 requires of syntax_error: what was
// expected at that position, and what was found.
func (e *errSyntax) message() string {
	got := e.got.Kind.String()
	if e.got.Kind == LowerID || e.got.Kind == UpperID || e.got.Kind == Int || e.got.Kind == Float {
		got = strconv.Quote(e.got.Text)
	} else if e.got.Kind == Invalid {
		got = strconv.Quote(e.got.Text)
	}
	return fmt.Sprintf("expected %s, found %s", e.want, got)
}

// ParseFile parses one source file. It returns the file and at most one
// diagnostic; a file that fails to parse still yields whatever comments the
// lexer saw, because `fmt --check` reports a file it cannot parse rather than
// rewriting it.
func ParseFile(path, src string) (*File, []Diagnostic) {
	all := newLexer(src).tokenize()
	f := &File{Path: path, Src: src}
	toks := make([]Token, 0, len(all))
	for _, t := range all {
		if t.Kind == Comment {
			f.Comments = append(f.Comments, t)
			continue
		}
		toks = append(toks, t)
	}
	p := &parser{file: path, src: src, toks: toks}
	decls, err := p.parseFile()
	f.Decls = decls
	if err != nil {
		return f, []Diagnostic{{
			File: path, Line: err.pos.Line, Col: err.pos.Col,
			Code: CodeSyntax, Message: err.message(), Severity: SeverityError,
		}}
	}
	return f, nil
}

func (p *parser) cur() Token  { return p.toks[p.i] }
func (p *parser) next() Token { t := p.toks[p.i]; p.i++; return t }

// atWord reports whether the current token is the contextual keyword w.
func (p *parser) atWord(w string) bool {
	t := p.cur()
	return t.Kind == LowerID && t.Text == w
}

func (p *parser) fail(want string) *errSyntax {
	t := p.cur()
	return &errSyntax{pos: t.Pos, want: want, got: t}
}

func (p *parser) expect(k Kind, want string) (Token, *errSyntax) {
	if p.cur().Kind != k {
		return Token{}, p.fail(want)
	}
	return p.next(), nil
}

func (p *parser) parseFile() ([]Decl, *errSyntax) {
	var out []Decl
	for p.cur().Kind != EOF {
		d, err := p.parseDecl()
		if err != nil {
			return out, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (p *parser) parseDecl() (Decl, *errSyntax) {
	switch {
	case p.atWord("pack"):
		return p.parsePack()
	case p.atWord("zone"):
		return p.parseZone()
	case p.atWord("template"):
		return p.parseTemplate()
	}
	return nil, p.fail(`"pack", "zone", or "template"`)
}

// parsePackRef reads `a.b.c` — lowercase segments, no PascalCase tail.
func (p *parser) parsePackRef() (string, Pos, *errSyntax) {
	first, err := p.expect(LowerID, "a pack name")
	if err != nil {
		return "", Pos{}, err
	}
	var sb strings.Builder
	sb.WriteString(first.Text)
	for p.cur().Kind == Dot {
		p.next()
		seg, err := p.expect(LowerID, "a pack name segment")
		if err != nil {
			return "", Pos{}, err
		}
		sb.WriteByte('.')
		sb.WriteString(seg.Text)
	}
	return sb.String(), first.Pos, nil
}

func (p *parser) parsePack() (*PackDecl, *errSyntax) {
	kw := p.next()
	name, namePos, err := p.parsePackRef()
	if err != nil {
		return nil, err
	}
	d := &PackDecl{Pos: kw.Pos, Name: name, NamePos: namePos}
	if p.atWord("requires") {
		p.next()
		rp, rpPos, err := p.parsePackRef()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(At, `"@"`); err != nil {
			return nil, err
		}
		v, err := p.expect(Int, "a version number")
		if err != nil {
			return nil, err
		}
		n, convErr := strconv.ParseUint(v.Text, 10, 32)
		if convErr != nil {
			return nil, &errSyntax{pos: v.Pos, want: "a version number", got: v}
		}
		d.Requires = &RequiresClause{Pack: rp, PackPos: rpPos, Version: uint32(n), VersionPos: v.Pos}
	}
	return d, nil
}

func (p *parser) parseZone() (*ZoneDecl, *errSyntax) {
	kw := p.next()
	id, err := p.expect(LowerID, "a Zone id")
	if err != nil {
		return nil, err
	}
	name, err := p.expectString("a Zone name")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(LBrace, `"{"`); err != nil {
		return nil, err
	}
	z := &ZoneDecl{Pos: kw.Pos, ID: id.Text, IDPos: id.Pos, Name: name.Value}
	for p.cur().Kind != RBrace {
		if p.cur().Kind == EOF {
			return nil, p.fail(`"}"`)
		}
		switch {
		case p.atWord("fallback"):
			f := p.parseFallback()
			z.Fallbacks = append(z.Fallbacks, f)
			z.Items = append(z.Items, f)
		case p.atWord("component"):
			c, err := p.parseComponent()
			if err != nil {
				return nil, err
			}
			z.Components = append(z.Components, c)
			z.Items = append(z.Items, c)
		case p.atWord("room"):
			r, err := p.parseRoom()
			if err != nil {
				return nil, err
			}
			z.Rooms = append(z.Rooms, r)
			z.Items = append(z.Items, r)
		default:
			return nil, p.fail(`"fallback", "component", "room", or "}"`)
		}
	}
	p.next() // }
	return z, nil
}

func (p *parser) parseFallback() *FallbackDecl {
	kw := p.next()
	// The grammar says LOWER_ID; a missing one is caught by the caller's next
	// iteration, which reports at the token that is actually there.
	if p.cur().Kind != LowerID {
		return &FallbackDecl{Pos: kw.Pos, RefPos: p.cur().Pos}
	}
	id := p.next()
	return &FallbackDecl{Pos: kw.Pos, Room: id.Text, RefPos: id.Pos}
}

func (p *parser) parseRoom() (*RoomDecl, *errSyntax) {
	kw := p.next()
	id, err := p.expect(LowerID, "a Room id")
	if err != nil {
		return nil, err
	}
	title, err := p.expectString("a Room title")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(LBrace, `"{"`); err != nil {
		return nil, err
	}
	r := &RoomDecl{Pos: kw.Pos, ID: id.Text, IDPos: id.Pos, Title: title.Value}
	for p.cur().Kind != RBrace {
		if p.cur().Kind == EOF {
			return nil, p.fail(`"}"`)
		}
		switch {
		case p.atWord("desc"):
			d, err := p.parseDesc()
			if err != nil {
				return nil, err
			}
			r.Descs = append(r.Descs, d)
			r.Items = append(r.Items, d)
		case p.atWord("exit"):
			e, err := p.parseExit()
			if err != nil {
				return nil, err
			}
			r.Exits = append(r.Exits, e)
			r.Items = append(r.Items, e)
		case p.atWord("component"):
			c, err := p.parseComponent()
			if err != nil {
				return nil, err
			}
			r.Components = append(r.Components, c)
			r.Items = append(r.Items, c)
		default:
			return nil, p.fail(`"desc", "exit", "component", or "}"`)
		}
	}
	p.next() // }
	return r, nil
}

func (p *parser) parseDesc() (*DescDecl, *errSyntax) {
	kw := p.next()
	d := &DescDecl{Pos: kw.Pos}
	first, err := p.expectString("a description")
	if err != nil {
		return nil, err
	}
	d.Parts = append(d.Parts, first)
	for p.cur().Kind == String {
		lit, err := p.expectString("a description")
		if err != nil {
			return nil, err
		}
		d.Parts = append(d.Parts, lit)
	}
	parts := make([]string, len(d.Parts))
	for i, s := range d.Parts {
		parts[i] = s.Value
		if d.BadEsc == nil {
			if pos, esc, bad := badEscape(s); bad {
				d.BadEsc, d.BadEscWh = &pos, esc
			}
		}
	}
	d.Value = strings.Join(parts, " ")
	return d, nil
}

func (p *parser) parseExit() (*ExitDecl, *errSyntax) {
	kw := p.next()
	dir, err := p.expect(LowerID, "a Direction")
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != Arrow {
		return nil, p.fail(`"->"`)
	}
	p.next()
	target, err := p.expect(LowerID, "a Room")
	if err != nil {
		return nil, err
	}
	e := &ExitDecl{Pos: kw.Pos, Direction: dir.Text, DirPos: dir.Pos, ToRoom: target.Text, RefPos: target.Pos}
	if p.cur().Kind == Dot {
		p.next()
		room, err := p.expect(LowerID, "a Room")
		if err != nil {
			return nil, err
		}
		e.ToZone, e.ToRoom = target.Text, room.Text
	}
	for p.atWord("perceives") {
		p.next()
		if _, err := p.expect(LBracket, `"["`); err != nil {
			return nil, err
		}
		e.PerceivesSet = true
		s, err := p.expect(LowerID, "a sense")
		if err != nil {
			return nil, err
		}
		e.Perceives = append(e.Perceives, Sense{Name: s.Text, Pos: s.Pos})
		for p.cur().Kind == Comma {
			p.next()
			s, err := p.expect(LowerID, "a sense")
			if err != nil {
				return nil, err
			}
			e.Perceives = append(e.Perceives, Sense{Name: s.Text, Pos: s.Pos})
		}
		if _, err := p.expect(RBracket, `"]"`); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (p *parser) parseTemplate() (*TemplateDecl, *errSyntax) {
	kw := p.next()
	name, err := p.expect(UpperID, "a PascalCase Template name")
	if err != nil {
		return nil, err
	}
	t := &TemplateDecl{Pos: kw.Pos, Name: name.Text, NamePos: name.Pos}
	for {
		switch {
		case p.atWord("kind"):
			head := p.next()
			k, err := p.expect(LowerID, `"entity", "item", or "behavior"`)
			if err != nil {
				return nil, err
			}
			switch k.Text {
			case "entity", "item", "behavior":
			default:
				return nil, &errSyntax{pos: k.Pos, want: `"entity", "item", or "behavior"`, got: k}
			}
			t.Heads = append(t.Heads, TemplateHead{Pos: head.Pos, Kind: k.Text, RefPos: k.Pos})
			continue
		case p.atWord("extends"):
			head := p.next()
			ref, refPos, err := p.parseTemplateRef()
			if err != nil {
				return nil, err
			}
			t.Heads = append(t.Heads, TemplateHead{Pos: head.Pos, Extends: ref, RefPos: refPos})
			continue
		}
		break
	}
	if _, err := p.expect(LBrace, `"kind", "extends", or "{"`); err != nil {
		return nil, err
	}
	for p.cur().Kind != RBrace {
		if p.cur().Kind == EOF {
			return nil, p.fail(`"}"`)
		}
		switch {
		case p.atWord("component"):
			c, err := p.parseComponent()
			if err != nil {
				return nil, err
			}
			t.Components = append(t.Components, c)
			t.Items = append(t.Items, c)
		case p.atWord("remove"):
			r, err := p.parseRemove()
			if err != nil {
				return nil, err
			}
			t.Removes = append(t.Removes, r)
			t.Items = append(t.Items, r)
		default:
			return nil, p.fail(`"component", "remove", or "}"`)
		}
	}
	p.next() // }
	return t, nil
}

// parseTemplateRef reads `(LOWER_ID ".")* UPPER_ID`.
func (p *parser) parseTemplateRef() (string, Pos, *errSyntax) {
	start := p.cur().Pos
	var sb strings.Builder
	for p.cur().Kind == LowerID {
		sb.WriteString(p.next().Text)
		if p.cur().Kind != Dot {
			return "", Pos{}, p.fail(`"." before a PascalCase name`)
		}
		p.next()
		sb.WriteByte('.')
	}
	name, err := p.expect(UpperID, "a PascalCase Template name")
	if err != nil {
		return "", Pos{}, err
	}
	sb.WriteString(name.Text)
	return sb.String(), start, nil
}

// parseComponentRef reads `(LOWER_ID ".")+ UPPER_ID` — always fully qualified.
// There is no import, alias, or `using` in v1: a Component's pack is its
// provenance (semantics.md §3).
func (p *parser) parseComponentRef() (string, Pos, *errSyntax) {
	start := p.cur().Pos
	if p.cur().Kind != LowerID {
		return "", Pos{}, p.fail("a fully qualified Component type")
	}
	var sb strings.Builder
	for p.cur().Kind == LowerID {
		sb.WriteString(p.next().Text)
		if p.cur().Kind != Dot {
			return "", Pos{}, p.fail(`"." before a PascalCase type name`)
		}
		p.next()
		sb.WriteByte('.')
	}
	name, err := p.expect(UpperID, "a PascalCase Component type name")
	if err != nil {
		return "", Pos{}, err
	}
	sb.WriteString(name.Text)
	return sb.String(), start, nil
}

func (p *parser) parseComponent() (*ComponentDecl, *errSyntax) {
	kw := p.next()
	ref, refPos, err := p.parseComponentRef()
	if err != nil {
		return nil, err
	}
	open, err2 := p.expect(LBrace, `"{"`)
	if err2 != nil {
		return nil, err2
	}
	c := &ComponentDecl{Pos: kw.Pos, Type: ref, TypePos: refPos}
	for p.cur().Kind != RBrace {
		if p.cur().Kind == EOF {
			return nil, p.fail(`"}"`)
		}
		name, err := p.expect(LowerID, "a field name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(Colon, `":"`); err != nil {
			return nil, err
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		c.Fields = append(c.Fields, &FieldAssign{Name: name.Text, NamePos: name.Pos, Value: v})
	}
	closing := p.next() // }
	c.MultiLine = closing.Pos.Line != open.Pos.Line
	return c, nil
}

func (p *parser) parseRemove() (*RemoveDecl, *errSyntax) {
	kw := p.next()
	switch {
	case p.atWord("component"):
		p.next()
		ref, _, err := p.parseComponentRef()
		if err != nil {
			return nil, err
		}
		return &RemoveDecl{Pos: kw.Pos, Component: ref}, nil
	case p.atWord("field"):
		p.next()
		name, err := p.expect(LowerID, "a field name")
		if err != nil {
			return nil, err
		}
		if !p.atWord("from") {
			return nil, p.fail(`"from"`)
		}
		p.next()
		ref, _, err2 := p.parseComponentRef()
		if err2 != nil {
			return nil, err2
		}
		return &RemoveDecl{Pos: kw.Pos, Field: name.Text, Component: ref}, nil
	}
	return nil, p.fail(`"component" or "field"`)
}

func (p *parser) parseValue() (Value, *errSyntax) {
	t := p.cur()
	switch t.Kind {
	case String:
		lit, err := p.expectString("a value")
		if err != nil {
			return Value{}, err
		}
		v := Value{Kind: String, Pos: lit.Pos, Raw: lit.Raw, Str: lit.Value}
		if pos, esc, bad := badEscape(lit); bad {
			v.BadEsc, v.BadEscWh = &pos, esc
		}
		return v, nil
	case Int:
		p.next()
		n, err := strconv.ParseInt(t.Text, 10, 64)
		if err != nil {
			return Value{}, &errSyntax{pos: t.Pos, want: "an int64", got: t}
		}
		return Value{Kind: Int, Pos: t.Pos, Raw: t.Text, Int: n}, nil
	case Float:
		p.next()
		return Value{Kind: Float, Pos: t.Pos, Raw: t.Text}, nil
	case LowerID:
		if t.Text == "true" || t.Text == "false" {
			p.next()
			return Value{Kind: LowerID, Pos: t.Pos, Raw: t.Text, Bool: t.Text == "true"}, nil
		}
	}
	return Value{}, p.fail("a string, an integer, or true/false")
}

// expectString consumes a string literal and decodes it. An unterminated
// literal reports at its opening quote, which is where the corpus's syntax
// sidecars name it (unterminated-string.aw:2:8).
func (p *parser) expectString(want string) (StringLit, *errSyntax) {
	t := p.cur()
	if t.Kind != String {
		return StringLit{}, p.fail(want)
	}
	if len(t.Text) < 2 || !strings.HasSuffix(t.Text, `"`) {
		return StringLit{}, &errSyntax{pos: t.Pos, want: "a closing quote", got: t}
	}
	p.next()
	return StringLit{Pos: t.Pos, Raw: t.Text, Value: decodeString(t.Text)}, nil
}

// decodeString applies the escape set the language has — \n, \" and \\ — and
// leaves any other escape as written. An escape outside the set is
// invalid_escape, raised by the resolver against BadEsc rather than here,
// because it is a semantic finding: the corpus case parses (corpus README,
// invalid/semantic/invalid-escape/).
func decodeString(raw string) string {
	body := strings.TrimSuffix(strings.TrimPrefix(raw, `"`), `"`)
	var sb strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' || i+1 >= len(body) {
			sb.WriteByte(body[i])
			continue
		}
		i++
		switch body[i] {
		case 'n':
			sb.WriteByte('\n')
		case '"':
			sb.WriteByte('"')
		case '\\':
			sb.WriteByte('\\')
		default:
			sb.WriteByte('\\')
			sb.WriteByte(body[i])
		}
	}
	return sb.String()
}

// badEscape finds the first escape outside \n, \" and \\, returning the
// position of the backslash — not of the string's opening quote, which is what
// corpus/invalid/semantic/invalid-escape/ names (z.aw:3:12).
func badEscape(lit StringLit) (Pos, string, bool) {
	col := lit.Pos.Col
	body := strings.TrimSuffix(strings.TrimPrefix(lit.Raw, `"`), `"`)
	col++ // past the opening quote
	for i := 0; i < len(body); {
		r, n := utf8.DecodeRuneInString(body[i:])
		if r != '\\' || i+n >= len(body) {
			i += n
			col++
			continue
		}
		esc, en := utf8.DecodeRuneInString(body[i+n:])
		switch esc {
		case 'n', '"', '\\':
			i += n + en
			col += 2
			continue
		}
		return Pos{Line: lit.Pos.Line, Col: col}, `\` + string(esc), true
	}
	return Pos{}, "", false
}
