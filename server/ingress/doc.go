// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package ingress is the produce between the two halves of the Command
// Pipeline (AW-SRV-010): the moment an Intent stops being a client's
// assertion and becomes an ordered fact in the World's history.
//
// It implements gateway.Ingress. A Submit is rate limited per Session,
// queued behind the Session's earlier Submits so their offsets are in
// submission order, run through command.Pipeline — parse, authorize,
// produce — and answered with the Partition and offset the Command landed
// on. The produce is KafkaProducer: acks=all, the idempotent producer, the
// explicit sim.PartitionFor partitioner, and one deadline. When the log is
// unreachable the World is read-only, not down: Submit fails fast with
// UNAVAILABLE, andara_ingress_degraded reads 1, and the tick and the Event
// stream go on.
//
// Bindings is the Gateway's routing view of where each Session's Character
// is: bound by AW-SRV-014, kept current here from the sim's own Events, and
// what authorize reads to pick a Partition. While a Character is between
// Zones its Session's Intents are held, in order, until it arrives.
package ingress
