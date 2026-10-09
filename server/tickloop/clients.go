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
// copy of them. Each consumer is read_committed (client-contract.md).

// scanClientID is the client.id of the clients that carry none of their
// caller's: the Boundary scan, the commands fetch and the end-offset query.
const scanClientID = "andara-server"

func simConsumerOpts(brokers []string, clientID, topic string, assign map[int32]kgo.Offset) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID + "-sim"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}),
	}
}

func boundaryScanOpts(brokers []string, eventsTopic string) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(scanClientID + "-boundaries"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{eventsTopic: {BoundaryPartition: kgo.NewOffset().AtStart()}}),
	}
}

func fetchOpts(brokers []string, topic string, partition int32, from int64) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(scanClientID + "-fetch"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {partition: kgo.NewOffset().At(from)}}),
	}
}

func offsetsOpts(brokers []string) []kgo.Opt {
	return []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID(scanClientID + "-offsets")}
}

func boundaryAdminOpts(brokers []string, clientID string) []kgo.Opt {
	return []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID(clientID + "-boundaries")}
}

func boundaryProbeOpts(brokers []string, clientID, topic string, offset int64) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID + "-boundaries"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.FetchMaxBytes(256 << 10),
		kgo.FetchMaxPartitionBytes(128 << 10),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	}
}

func boundaryConsumerOpts(brokers []string, clientID, topic string, offset int64) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID + "-boundaries"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.FetchMaxBytes(16 << 20),
		kgo.FetchMaxPartitionBytes(4 << 20),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	}
}

func commandSourceOpts(brokers []string, clientID, topic string, assign map[int32]kgo.Offset) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID + "-commands"),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}),
	}
}

// Sites describes every Kafka client this package builds, for a server whose
// client.id base is clientID.
func Sites(brokers []string, clientID string, deliveryTimeout time.Duration) []kafkaclient.Site {
	at := map[int32]kgo.Offset{0: kgo.NewOffset().At(0)}
	return []kafkaclient.Site{
		{Name: "tick boundary publisher", Role: kafkaclient.Producer, Opts: publisherOpts(brokers, clientID, deliveryTimeout), Partitioning: kafkaclient.Manual},
		{Name: "tick loop command consumer", Role: kafkaclient.Consumer, Opts: simConsumerOpts(brokers, clientID, CommandsTopic, at)},
		{Name: "tick boundary scan", Role: kafkaclient.Consumer, Opts: boundaryScanOpts(brokers, EventsTopic), OneShot: kafkaclient.TickBoundaryScan},
		{Name: "commands fetch", Role: kafkaclient.Consumer, Opts: fetchOpts(brokers, CommandsTopic, 0, 0)},
		{Name: "end offset query", Role: kafkaclient.Admin, Opts: offsetsOpts(brokers)},
	}
}

// ReaderSites describes the clients the BoundaryReader and the CommandSource
// build for a caller whose client.id base is clientID: the server's recovery
// and the projector's replay.
func ReaderSites(brokers []string, clientID string) []kafkaclient.Site {
	at := map[int32]kgo.Offset{0: kgo.NewOffset().At(0)}
	return []kafkaclient.Site{
		{Name: clientID + " boundary reader", Role: kafkaclient.Admin, Opts: boundaryAdminOpts(brokers, clientID)},
		{Name: clientID + " boundary probe", Role: kafkaclient.Consumer, Opts: boundaryProbeOpts(brokers, clientID, EventsTopic, 0)},
		{Name: clientID + " boundary consumer", Role: kafkaclient.Consumer, Opts: boundaryConsumerOpts(brokers, clientID, EventsTopic, 0)},
		{Name: clientID + " command source", Role: kafkaclient.Consumer, Opts: commandSourceOpts(brokers, clientID, CommandsTopic, at)},
	}
}
