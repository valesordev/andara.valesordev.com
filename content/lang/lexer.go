// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang

import (
	"strings"
	"unicode/utf8"
)

// lexer turns source into tokens. It is deliberately not table-driven: the
// terminal set is nine characters and four regular classes, and a hand-written
// scanner is what makes the maximal-munch rules below readable.
//
// Two of those rules are contract, not convenience:
//
//   - INT is `-?(0|[1-9][0-9]*)`, so `007` lexes as three integers rather than
//     one. The parse then fails at the *second* token, which is the column the
//     corpus names (invalid/syntax/number-leading-zero.aw:3:25).
//   - STRING admits any `\<char>` so that `\t` reaches the resolver as
//     invalid_escape naming the escape, rather than failing here as an
//     unterminated string naming the rest of the file (grammar.ebnf, STRING).
type lexer struct {
	src  string
	off  int // byte offset
	line int
	col  int // in runes
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1, col: 1} }

func (l *lexer) pos() Pos { return Pos{Line: l.line, Col: l.col} }

func (l *lexer) peek() (rune, int) {
	if l.off >= len(l.src) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(l.src[l.off:])
}

func (l *lexer) advance() rune {
	r, n := l.peek()
	if n == 0 {
		return 0
	}
	l.off += n
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

// eofPos is where a parser reports input that ran out: one past the final
// character (errors.md rule 3). It is computed the same way
// scripts/content_grammar_check.py computes it, because the syntax sidecars
// were written against that function and AC-1 is a byte comparison.
func eofPos(src string) Pos {
	lines := strings.Split(src, "\n")
	last := lines[len(lines)-1]
	return Pos{Line: len(lines), Col: utf8.RuneCountInString(last) + 1}
}

func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isLowerRest(r rune) bool { return isLower(r) || isDigit(r) || r == '_' }
func isUpperRest(r rune) bool { return isLower(r) || isUpper(r) || isDigit(r) }

// tokenize scans the whole file. Comments are included; the parser filters them
// and the formatter does not.
//
// It never fails: a character no terminal admits becomes an Invalid token at
// its own position, so the parser reports it as a syntax error through the one
// path every other syntax error takes.
func (l *lexer) tokenize() []Token {
	var out []Token
	for {
		l.skipSpace()
		start := l.pos()
		r, n := l.peek()
		if n == 0 {
			out = append(out, Token{Kind: EOF, Pos: eofPos(l.src)})
			return out
		}
		switch {
		case r == '/' && strings.HasPrefix(l.src[l.off:], "//"):
			out = append(out, Token{Kind: Comment, Text: l.scanComment(), Pos: start})
		case r == '"':
			out = append(out, Token{Kind: String, Text: l.scanString(), Pos: start})
		case strings.HasPrefix(l.src[l.off:], "->"):
			// Before the number case: the `-` of an exit arrow is not the
			// sign of a negative integer.
			out = append(out, l.scanPunct(start))
		case isDigit(r) || r == '-':
			out = append(out, l.scanNumber(start))
		case isLower(r):
			out = append(out, Token{Kind: LowerID, Text: l.scanWhile(isLowerRest), Pos: start})
		case isUpper(r):
			out = append(out, Token{Kind: UpperID, Text: l.scanWhile(isUpperRest), Pos: start})
		default:
			out = append(out, l.scanPunct(start))
		}
	}
}

func (l *lexer) skipSpace() {
	for {
		r, n := l.peek()
		if n == 0 || (r != ' ' && r != '\t' && r != '\r' && r != '\n') {
			return
		}
		l.advance()
	}
}

func (l *lexer) scanWhile(ok func(rune) bool) string {
	start := l.off
	for {
		r, n := l.peek()
		if n == 0 || !ok(r) {
			return l.src[start:l.off]
		}
		l.advance()
	}
}

func (l *lexer) scanComment() string {
	start := l.off
	for {
		r, n := l.peek()
		if n == 0 || r == '\n' {
			return l.src[start:l.off]
		}
		l.advance()
	}
}

// scanString consumes a double-quoted literal. An unterminated one — end of
// line or end of file before the closing quote — returns everything it saw and
// is reported by the parser at the opening quote, which is the column the
// corpus names (invalid/syntax/unterminated-string.aw:2:8).
func (l *lexer) scanString() string {
	start := l.off
	l.advance() // opening quote
	for {
		r, n := l.peek()
		if n == 0 || r == '\n' {
			return l.src[start:l.off] // unterminated: no closing quote in Text
		}
		l.advance()
		if r == '\\' {
			if r2, n2 := l.peek(); n2 != 0 && r2 != '\n' {
				l.advance() // the escaped character, whatever it is
			}
			continue
		}
		if r == '"' {
			return l.src[start:l.off]
		}
	}
}

// scanNumber implements the INT and FLOAT terminals exactly. One spelling per
// value: a leading `+`, a leading zero, and `1_000` are not numbers, so each
// stops the scan and the parser reports the remainder as unexpected.
func (l *lexer) scanNumber(start Pos) Token {
	begin := l.off
	if r, _ := l.peek(); r == '-' {
		l.advance()
		if r2, n2 := l.peek(); n2 == 0 || !isDigit(r2) {
			return Token{Kind: Invalid, Text: l.src[begin:l.off], Pos: start}
		}
	}
	if r, _ := l.peek(); r == '0' {
		l.advance() // `0` alone; `007` is three tokens
	} else {
		l.scanWhile(isDigit)
	}
	// A FLOAT only where a digit follows the dot; otherwise the dot is a
	// separate token and `1.` is an integer followed by a dot.
	if r, _ := l.peek(); r == '.' {
		save := *l
		l.advance()
		if r2, n2 := l.peek(); n2 != 0 && isDigit(r2) {
			l.scanWhile(isDigit)
			return Token{Kind: Float, Text: l.src[begin:l.off], Pos: start}
		}
		*l = save
	}
	return Token{Kind: Int, Text: l.src[begin:l.off], Pos: start}
}

func (l *lexer) scanPunct(start Pos) Token {
	if strings.HasPrefix(l.src[l.off:], "->") {
		l.advance()
		l.advance()
		return Token{Kind: Arrow, Text: "->", Pos: start}
	}
	r := l.advance()
	kind := Invalid
	switch r {
	case '{':
		kind = LBrace
	case '}':
		kind = RBrace
	case '[':
		kind = LBracket
	case ']':
		kind = RBracket
	case ':':
		kind = Colon
	case ',':
		kind = Comma
	case '.':
		kind = Dot
	case '@':
		kind = At
	}
	return Token{Kind: kind, Text: string(r), Pos: start}
}
