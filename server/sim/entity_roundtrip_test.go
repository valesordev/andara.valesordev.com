// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim_test

import (
	"reflect"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

// The fields log.v1.Entity deliberately doesn't carry (AW-SRV-028): the Room
// (the Arrive names it), the dormant fields and the linkdead fields (a body in
// either never moves). Anything else on EntityState that the proto doesn't
// carry is a field the Entity loses when it crosses a Zone, and its hash
// differs after a boundary.
var notOnTheWire = map[string]bool{
	"Room": true, "Dormant": true, "DormantSince": true,
	"LinkdeadSince": true, "LinkdeadDeadline": true, "LinkdeadCeiling": true, "LinkdeadExtension": true,
}

// AC-10: EntityState → log.v1.Entity → EntityState over every field the wire
// carries. A field added to EntityState and not to the proto fails here,
// naming the field, rather than hashing differently after a boundary.
func TestEntityState_SurvivesTheWire(t *testing.T) {
	in := filledEntity(t)
	out := sim.EntityFromProto(in.Proto(), in.Room)
	v, w := reflect.ValueOf(in), reflect.ValueOf(out)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if notOnTheWire[name] {
			continue
		}
		if !reflect.DeepEqual(v.Field(i).Interface(), w.Field(i).Interface()) {
			t.Errorf("EntityState.%s did not survive log.v1.Entity: sent %#v, got %#v. Add it to the proto and to Proto and EntityFromProto, or to notOnTheWire here with the reason it never travels",
				name, v.Field(i).Interface(), w.Field(i).Interface())
		}
	}
}

// filledEntity sets every field of EntityState to a non-zero value, by kind,
// so a field added later is exercised without anyone editing this test. A
// kind it can't fill fails the test, saying so, rather than skipping the
// field.
func filledEntity(t *testing.T) sim.EntityState {
	t.Helper()
	var e sim.EntityState
	v := reflect.ValueOf(&e).Elem()
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.String:
			f.SetString(name + "-value")
		case reflect.Uint64, reflect.Uint32, reflect.Uint:
			f.SetUint(uint64(7 + i))
		case reflect.Int, reflect.Int64, reflect.Int32:
			f.SetInt(int64(7 + i))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			if f.Type().Elem() != reflect.TypeOf(sim.Component{}) {
				t.Fatalf("EntityState.%s is a slice of %s: teach filledEntity to fill it", name, f.Type().Elem())
			}
			f.Set(reflect.ValueOf([]sim.Component{{Type: "t.Comp", Fields: []sim.ComponentField{
				{Name: "a", Kind: sim.FieldString, Str: "s"}, {Name: "b", Kind: sim.FieldInt, Int: 9}, {Name: "c", Kind: sim.FieldBool, Bool: true},
			}}}))
		default:
			t.Fatalf("EntityState.%s has kind %s: teach filledEntity to fill it", name, f.Kind())
		}
	}
	return e
}
