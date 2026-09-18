// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

// RNG is the simulation's only source of randomness: xoshiro256** seeded
// through splitmix64, with its whole state readable and restorable. It is
// part of World state — the State Hash covers it, a snapshot carries it —
// because a random draw the log cannot reproduce is a fork in history.
//
// Hand-rolled rather than math/rand: the standard library's generators are
// not promised stable across Go releases and their global instances are
// seeded from the wall clock, which is exactly what depguard denies the
// core (ADR-0002). The algorithm is Blackman and Vigna's; the outputs are
// pinned by a golden test so an accidental edit here is a test failure, not
// a replay that silently diverges.
type RNG struct {
	s [4]uint64
}

// NewRNG seeds a generator. The same seed yields the same sequence on every
// platform and every build.
func NewRNG(seed uint64) *RNG {
	r := &RNG{}
	x := seed
	for i := range r.s {
		// splitmix64
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		r.s[i] = z ^ (z >> 31)
	}
	return r
}

// Uint64 returns the next value.
func (r *RNG) Uint64() uint64 {
	s := &r.s
	result := rotl(s[1]*5, 7) * 9
	t := s[1] << 17
	s[2] ^= s[0]
	s[3] ^= s[1]
	s[1] ^= s[2]
	s[0] ^= s[3]
	s[2] ^= t
	s[3] = rotl(s[3], 45)
	return result
}

// Intn returns a value in [0, n). n must be positive. Rejection sampling
// keeps the distribution uniform, and the loop bound is deterministic for a
// given state, so replay draws the same number of values.
func (r *RNG) Intn(n int64) int64 {
	if n <= 0 {
		panic("sim: Intn with non-positive bound")
	}
	bound := uint64(n)
	limit := ^uint64(0) - (^uint64(0) % bound)
	for {
		v := r.Uint64()
		if v < limit {
			return int64(v % bound)
		}
	}
}

// State returns the generator's state, for the State Hash and snapshots.
func (r *RNG) State() [4]uint64 { return r.s }

// Restore sets the generator's state, for recovery from a snapshot.
func (r *RNG) Restore(s [4]uint64) { r.s = s }

func rotl(x uint64, k uint) uint64 { return (x << k) | (x >> (64 - k)) }
