// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"encoding/json"

	"github.com/valesordev/andara/server/sim"
)

// zonePositions walks a Zone Definition file a second time and records the
// source line of each Room, Exit, and Component.
//
// It is a second pass because protojson has thrown positions away by the time a
// ZoneDefinition exists, and the sim core — which owns validation and must stay
// dependency-free (ADR-0001, CLAUDE.md §10) — cannot go back to the bytes. The
// story asks for a file *and a line* on a rejected Direction (AC-6), and the
// glossary promises it to Builders, so somebody has to keep them; this is the
// only layer that still has them.
//
// It never fails the load. A file that cannot be walked yields whatever it got
// to before stopping, and a missing position is line 0 — "not line-scoped",
// which is what every AW-SRV-001 finding reported. Losing a line number must not
// be able to turn valid content into a boot failure.
func zonePositions(data []byte) *sim.Positions {
	s := &posScanner{dec: json.NewDecoder(bytes.NewReader(data)), data: data}
	tok, err := s.dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	pos := &sim.Positions{}
	for s.dec.More() {
		key, err := s.key()
		if err != nil {
			return pos
		}
		switch key {
		case "rooms":
			pos.Rooms, err = s.rooms()
		case "components":
			pos.ZoneComponents, err = s.elementLines()
		default:
			err = s.skipValue()
		}
		if err != nil {
			return pos
		}
	}
	return pos
}

// posScanner walks JSON tokens while keeping the byte offsets the decoder
// reports, so a value's position can be turned back into a line.
type posScanner struct {
	dec  *json.Decoder
	data []byte
}

// line is the source line of the token just read. InputOffset lands just past
// that token, so for an opening brace it is the line the value starts on.
func (s *posScanner) line() int {
	return lineFromOffset(s.data, s.dec.InputOffset())
}

func (s *posScanner) key() (string, error) {
	tok, err := s.dec.Token()
	if err != nil {
		return "", err
	}
	k, _ := tok.(string)
	return k, nil
}

// skipValue consumes one complete value and discards it.
func (s *posScanner) skipValue() error {
	tok, err := s.dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		return s.skipRest()
	}
	return nil
}

// skipRest consumes to the delimiter matching one already read.
func (s *posScanner) skipRest() error {
	for depth := 1; depth > 0; {
		tok, err := s.dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// elementLines records the line each element of an array starts on, indexed the
// way the repeated protobuf field is. Serves `exits` and both `components`
// arrays, which differ only in what the sim does with the indices.
func (s *posScanner) elementLines() ([]int, error) {
	tok, err := s.dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '[' {
		// Not an array. protojson will reject it with its own message; the
		// position walk has nothing to say about a shape that has no elements.
		if ok && d == '{' {
			return nil, s.skipRest()
		}
		return nil, nil
	}
	var lines []int
	for s.dec.More() {
		el, err := s.dec.Token()
		if err != nil {
			return lines, err
		}
		lines = append(lines, s.line())
		if ed, ok := el.(json.Delim); ok && (ed == '{' || ed == '[') {
			if err := s.skipRest(); err != nil {
				return lines, err
			}
		}
	}
	_, err = s.dec.Token() // closing ]
	return lines, err
}

// rooms records each Room's line and, within it, the lines of its Exits and
// Components.
func (s *posScanner) rooms() ([]sim.RoomPositions, error) {
	tok, err := s.dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '[' {
		if ok && d == '{' {
			return nil, s.skipRest()
		}
		return nil, nil
	}
	var out []sim.RoomPositions
	for s.dec.More() {
		el, err := s.dec.Token()
		if err != nil {
			return out, err
		}
		rp := sim.RoomPositions{Line: s.line()}
		ed, isDelim := el.(json.Delim)
		if !isDelim || ed != '{' {
			if isDelim && ed == '[' {
				if err := s.skipRest(); err != nil {
					return out, err
				}
			}
			out = append(out, rp)
			continue
		}
		for s.dec.More() {
			key, err := s.key()
			if err != nil {
				out = append(out, rp)
				return out, err
			}
			switch key {
			case "exits":
				rp.Exits, err = s.elementLines()
			case "components":
				rp.Components, err = s.elementLines()
			default:
				err = s.skipValue()
			}
			if err != nil {
				out = append(out, rp)
				return out, err
			}
		}
		if _, err := s.dec.Token(); err != nil { // closing }
			out = append(out, rp)
			return out, err
		}
		out = append(out, rp)
	}
	_, err = s.dec.Token() // closing ]
	return out, err
}
