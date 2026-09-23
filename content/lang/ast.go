// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

// The AST. Every node carries the position of the token a finding about it must
// point at, which is not always the node's first token: errors.md rule 1 says
// the position names the offending *value*, so a Template carries both the
// `template` keyword's position and its name's, and an Exit carries the
// keyword, the direction, and the target separately.
//
// The formatter walks this tree in source order and the decompiler builds one,
// so the tree is the single representation all three entry points share — which
// is the whole reason the story asks for one package rather than three tools
// (story Context).

// File is one parsed *.aw source.
type File struct {
	Path     string // relative to the pack directory, slash-separated
	Src      string // the bytes as authored, for the src/ blob and for fmt
	Decls    []Decl
	Comments []Token // every comment in the file, in source order, for fmt
}

// Decl is a top-level declaration: pack, zone, or template.
type Decl interface{ declPos() Pos }

// PackDecl is `pack <ref> [requires <ref>@<int>]`. Exactly one exists across a
// pack (semantics.md §2).
type PackDecl struct {
	Pos      Pos // the `pack` keyword
	Name     string
	NamePos  Pos
	Requires *RequiresClause // nil for andara.core, which is the root
}

// RequiresClause is `requires <pack>@<version>`. Version carries its own
// position because core_version_mismatch points at the integer.
type RequiresClause struct {
	Pack       string
	PackPos    Pos
	Version    uint32
	VersionPos Pos
}

func (d *PackDecl) declPos() Pos { return d.Pos }

// ZoneDecl is `zone <id> "<name>" { … }`.
type ZoneDecl struct {
	Pos        Pos // the `zone` keyword
	ID         string
	IDPos      Pos
	Name       string
	Fallbacks  []*FallbackDecl // more than one is duplicate_declaration
	Components []*ComponentDecl
	Rooms      []*RoomDecl
	Items      []ZoneItem // every item in source order, for fmt
}

func (d *ZoneDecl) declPos() Pos { return d.Pos }

// ZoneItem is a fallback, a component, or a room — kept in source order so the
// formatter can reproduce a Builder's layout (formatting.md §1: fmt does not
// reorder).
type ZoneItem interface{ itemPos() Pos }

// FallbackDecl is `fallback <room>`. PENDING AW-SRV-012 (semantics.md §9).
type FallbackDecl struct {
	Pos    Pos // the `fallback` keyword
	Room   string
	RefPos Pos
}

func (d *FallbackDecl) itemPos() Pos { return d.Pos }

// RoomDecl is `room <id> "<title>" { … }`.
type RoomDecl struct {
	Pos        Pos // the `room` keyword
	ID         string
	IDPos      Pos
	Title      string
	Descs      []*DescDecl // more than one is duplicate_declaration
	Exits      []*ExitDecl
	Components []*ComponentDecl
	Items      []RoomItem // source order, for fmt
}

func (d *RoomDecl) itemPos() Pos { return d.Pos }

// RoomItem is a desc, an exit, or a component, in source order.
type RoomItem interface{ roomItemPos() Pos }

// DescDecl is `desc "…" ["…"]`: one or more adjacent literals joined with a
// single space (semantics.md §1). Parts keeps them separate so fmt can re-wrap.
type DescDecl struct {
	Pos      Pos // the `desc` keyword
	Parts    []StringLit
	Value    string // the parts joined with a single space
	BadEsc   *Pos   // the first invalid escape, if any
	BadEscWh string // the escape as written, e.g. `\t`
}

func (d *DescDecl) roomItemPos() Pos { return d.Pos }

// StringLit is one string literal: the decoded value plus the position and raw
// spelling fmt reproduces.
type StringLit struct {
	Pos   Pos
	Raw   string // with quotes and escapes as authored
	Value string // decoded
}

// ExitDecl is `exit <direction> -> <ref> [perceives [ … ]]`.
type ExitDecl struct {
	Pos          Pos // the `exit` keyword
	Direction    string
	DirPos       Pos
	ToZone       string // empty means the containing Zone (zone.proto's convention)
	ToRoom       string
	RefPos       Pos // the whole reference, including the `<zone>.` half
	Perceives    []Sense
	PerceivesSet bool
}

func (d *ExitDecl) roomItemPos() Pos { return d.Pos }

// Sense is one entry in a `perceives` list. PENDING AW-SRV-029.
type Sense struct {
	Name string
	Pos  Pos
}

// TemplateDecl is `template <Name> (kind <k> | extends <Ref>) { … }`.
//
// Heads is every head the Builder wrote, because the grammar admits any number
// and semantics requires exactly one: "expected { but found extends" teaches
// nothing about the model, where template_head says a root states its kind and
// a subtype inherits it (grammar.ebnf, template_decl).
type TemplateDecl struct {
	Pos        Pos // the `template` keyword
	Name       string
	NamePos    Pos
	Heads      []TemplateHead
	Components []*ComponentDecl
	Removes    []*RemoveDecl
	Items      []TemplateItem // source order, for fmt
}

func (d *TemplateDecl) declPos() Pos { return d.Pos }

// TemplateItem is a component or a remove, in source order.
type TemplateItem interface{ templateItemPos() Pos }

// TemplateHead is one `kind <k>` or `extends <Ref>`.
type TemplateHead struct {
	Pos     Pos    // the `kind` or `extends` keyword
	Kind    string // set when this head is a kind
	Extends string // set when this head is an extends
	RefPos  Pos    // the kind or reference token
}

// ComponentDecl is `component <pack>.<Type> { <field>: <value> … }`.
type ComponentDecl struct {
	Pos       Pos // the `component` keyword
	Type      string
	TypePos   Pos
	Fields    []*FieldAssign
	MultiLine bool // as authored: fmt neither collapses nor expands (formatting.md §3)
}

func (d *ComponentDecl) itemPos() Pos         { return d.Pos }
func (d *ComponentDecl) roomItemPos() Pos     { return d.Pos }
func (d *ComponentDecl) templateItemPos() Pos { return d.Pos }

// RemoveDecl is `remove component <ref>` or `remove field <name> from <ref>`.
// Present only to be rejected: a subtype may never remove, and a language with
// no removal syntax at all would make that rule unteachable (semantics.md §5).
type RemoveDecl struct {
	Pos       Pos    // the `remove` keyword
	Field     string // empty for `remove component`
	Component string
}

func (d *RemoveDecl) templateItemPos() Pos { return d.Pos }

// FieldAssign is `<name>: <value>`.
type FieldAssign struct {
	Name    string
	NamePos Pos
	Value   Value
}

// Value is a string, int, or bool. Float is parsed so that `3.5` is reported as
// float_literal with the fixed-units advice, rather than as a parse error
// pointing at a dot (grammar.ebnf, value).
type Value struct {
	Kind     Kind // String, Int, Float, or LowerID for a bool
	Pos      Pos
	Raw      string
	Str      string
	Int      int64
	Bool     bool
	BadEsc   *Pos
	BadEscWh string
}
