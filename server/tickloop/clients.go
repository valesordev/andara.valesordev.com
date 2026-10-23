// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/kafkaclient"
)

// The options of every Kafka client this package builds, in one place so
// config-assert (AW-SRV-053) checks the options the code builds with, not a
// copy of them. Each helper names its client (kafkaclient.Config.For) and
// returns the options beyond the shared ones kafkaclient.New supplies. Each
// consumer is read_committed (client-contract.md).

// scanClientID is the client.id of the clients that carry none of their
// caller's: the Boundary scan, the commands fetch and the end-offset query.
const scanClientID = "andara-server"

func simConsumerOpts(kc kafkaclient.Config, clientID, topic string, assign map[int32]kgo.Offset) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(clientID + "-sim"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}),
	}
}

func boundaryScanOpts(kc kafkaclient.Config, eventsTopic string) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(scanClientID + "-boundaries"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{eventsTopic: {BoundaryPartition: kgo.NewOffset().AtStart()}}),
	}
}

func fetchOpts(kc kafkaclient.Config, topic string, partition int32, from int64) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(scanClientID + "-fetch"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {partition: kgo.NewOffset().At(from)}}),
	}
}

func offsetsOpts(kc kafkaclient.Config) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(scanClientID + "-offsets"), nil
}

func boundaryAdminOpts(kc kafkaclient.Config, clientID string) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(clientID + "-boundaries"), nil
}

func boundaryProbeOpts(kc kafkaclient.Config, clientID, topic string, offset int64) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(clientID + "-boundaries"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.FetchMaxBytes(256 << 10),
		kgo.FetchMaxPartitionBytes(128 << 10),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	}
}

func boundaryConsumerOpts(kc kafkaclient.Config, clientID, topic string, offset int64) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(clientID + "-boundaries"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.FetchMaxBytes(16 << 20),
		kgo.FetchMaxPartitionBytes(4 << 20),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	}
}

func commandSourceOpts(kc kafkaclient.Config, clientID, topic string, assign map[int32]kgo.Offset) (kafkaclient.Config, []kgo.Opt) {
	return kc.For(clientID + "-commands"), []kgo.Opt{
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}),
	}
}

// Sites describes every Kafka client this package builds, for a server whose
// client.id base is clientID, on the topics t names.
func Sites(kc kafkaclient.Config, clientID string, deliveryTimeout time.Duration, t kafkaclient.Topics) []kafkaclient.Site {
	at := map[int32]kgo.Offset{0: kgo.NewOffset().At(0)}
	return []kafkaclient.Site{
		{Name: "tick boundary publisher", Role: kafkaclient.Producer, Opts: kafkaclient.SiteOpts(publisherOpts(kc, clientID, deliveryTimeout)), Partitioning: kafkaclient.Manual},
		{Name: "tick loop command consumer", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(simConsumerOpts(kc, clientID, t.Commands(), at))},
		{Name: "tick boundary scan", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(boundaryScanOpts(kc, t.Events())), OneShot: kafkaclient.TickBoundaryScan},
		{Name: "commands fetch", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(fetchOpts(kc, t.Commands(), 0, 0))},
		{Name: "end offset query", Role: kafkaclient.Admin, Opts: kafkaclient.SiteOpts(offsetsOpts(kc))},
	}
}

// ReaderSites describes the clients the BoundaryReader and the CommandSource
// build for a caller whose client.id base is clientID: the server's recovery
// and the projector's replay.
func ReaderSites(kc kafkaclient.Config, clientID string, t kafkaclient.Topics) []kafkaclient.Site {
	at := map[int32]kgo.Offset{0: kgo.NewOffset().At(0)}
	return []kafkaclient.Site{
		{Name: clientID + " boundary reader", Role: kafkaclient.Admin, Opts: kafkaclient.SiteOpts(boundaryAdminOpts(kc, clientID))},
		{Name: clientID + " boundary probe", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(boundaryProbeOpts(kc, clientID, t.Events(), 0))},
		{Name: clientID + " boundary consumer", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(boundaryConsumerOpts(kc, clientID, t.Events(), 0))},
		{Name: clientID + " command source", Role: kafkaclient.Consumer, Opts: kafkaclient.SiteOpts(commandSourceOpts(kc, clientID, t.Commands(), at))},
	}
}
