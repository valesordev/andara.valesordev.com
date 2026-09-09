package sim

import "hash/fnv"

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
