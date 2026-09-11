// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package canonical is the encoder for anything that feeds the State Hash
// (ADR-0007 rule 3, AW-SRV-005 AC-2).
//
// AW-SRV-020 states the rules and shapes the schema so they are satisfiable:
// no map fields, no float or double, no google.protobuf.Any in andara.log.v1 or
// andara.state.v1. This package is the encoder that honors them. It refuses a
// message whose descriptor breaks a rule rather than encoding it anyway,
// because a hash over a non-canonical encoding is a hash that two correct
// binaries can disagree on, and that disagreement would surface as a replay
// divergence months after the record was written.
package canonical

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Marshal serializes m deterministically: the same value produces the same
// bytes in every process, every time. It returns an error for a message whose
// descriptor contains a map, a float, a double, or a google.protobuf.Any
// anywhere in its type graph, before encoding a single byte.
func Marshal(m proto.Message) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("canonical: nil message")
	}
	if err := Check(m.ProtoReflect().Descriptor()); err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// Check walks a message descriptor and reports the first field that violates
// the determinism rules, naming the message and the field. Nested and repeated
// message fields are followed; recursive types are visited once.
func Check(md protoreflect.MessageDescriptor) error {
	return check(md, map[protoreflect.FullName]bool{})
}

func check(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) error {
	if seen[md.FullName()] {
		return nil
	}
	seen[md.FullName()] = true
	if md.FullName() == "google.protobuf.Any" {
		return fmt.Errorf("canonical: %s: google.protobuf.Any depends on a type registry that can differ between binaries", md.FullName())
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.IsMap() {
			return fmt.Errorf("canonical: %s.%s: map fields have no defined encoding order", md.FullName(), f.Name())
		}
		switch f.Kind() {
		case protoreflect.FloatKind, protoreflect.DoubleKind:
			return fmt.Errorf("canonical: %s.%s: %s is not reproducible across platforms", md.FullName(), f.Name(), f.Kind())
		case protoreflect.MessageKind, protoreflect.GroupKind:
			if err := check(f.Message(), seen); err != nil {
				return err
			}
		}
	}
	return nil
}
