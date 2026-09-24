// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import "fmt"

// Kind classifies a token. The language has no reserved words (semantics.md
// §1): `entity`, `room`, and `remove` are keywords only in the position the
// grammar gives them, so the lexer never produces a keyword token. It produces
// LowerID and UpperID, and the parser matches text at a position.
//
// That is what lets a Builder call a Room `exit` and a Component field
// `template` without learning a list of names they may not use.
type Kind uint8

// The token kinds. Comment is produced for the formatter and filtered out for
// the parser (formatting.md §5: comments are preserved by fmt and dropped by
// compile).
const (
	EOF Kind = iota
	LowerID
	UpperID
	String
	Int
	Float
	LBrace
	RBrace
	LBracket
	RBracket
	Arrow
	Colon
	Comma
	Dot
	At
	Comment
	Invalid
)

func (k Kind) String() string {
	switch k {
	case EOF:
		return "end of input"
	case LowerID:
		return "a lowercase identifier"
	case UpperID:
		return "a PascalCase name"
	case String:
		return "a string"
	case Int:
		return "an integer"
	case Float:
		return "a number"
	case LBrace:
		return `"{"`
	case RBrace:
		return `"}"`
	case LBracket:
		return `"["`
	case RBracket:
		return `"]"`
	case Arrow:
		return `"->"`
	case Colon:
		return `":"`
	case Comma:
		return `","`
	case Dot:
		return `"."`
	case At:
		return `"@"`
	case Comment:
		return "a comment"
	default:
		return "an unrecognized character"
	}
}

// Pos is a source position. Col counts runes rather than bytes (errors.md rule
// 2), so a desc with an em dash in it does not shift every column after it.
type Pos struct {
	Line int // 1-based
	Col  int // 1-based, in runes
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Token is one lexeme with the position the Builder must look at.
//
// Text is the source spelling: for a String it still carries the quotes and the
// escapes as written, because `fmt` reproduces what was authored and
// invalid_escape has to name the escape (errors.md §3.1).
type Token struct {
	Kind Kind
	Text string
	Pos  Pos
}
