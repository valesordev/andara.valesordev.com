// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"hash/fnv"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// PartitionCount is the number of Kafka partitions on andara.commands.v1
// (ADR-0001, ADR-0002). A Zone maps to exactly one of them.
const PartitionCount int32 = 64

// MinFormatVersion and MaxFormatVersion are the closed range this loader accepts.
const (
	MinFormatVersion uint32 = 1
	MaxFormatVersion uint32 = 1
)

// PartitionFor maps a ZoneID onto [0, PartitionCount). FNV-1a 32 of the ID
// bytes, then modulo. AW-SRV-010 must use this function, not a Kafka client
// default: a library upgrade that remapped Zones would split their history.
func PartitionFor(id ZoneID) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int32(h.Sum32() % uint32(PartitionCount))
}

// WorldPartition is where a World-scoped Command — one with no zone_id, a
// ContentSwap (AW-SRV-012) — is produced. Any fixed Partition would do: the
// swap applies after every other record of its tick wherever it lands, and
// one Partition keeps two swaps in offset order.
const WorldPartition int32 = 0

// CommandPartition is the Partition a Command is produced to: its Zone's, or
// WorldPartition for a World-scoped one.
func CommandPartition(cmd *logv1.LoggedCommand) int32 {
	if cmd.GetZoneId() == "" {
		return WorldPartition
	}
	return PartitionFor(ZoneID(cmd.GetZoneId()))
}
