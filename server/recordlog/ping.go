// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recordlog

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Ping reports whether any broker cl knows answers, asking every
// discovered broker at once and returning at the first answer.
// kgo.Client.Ping asks them one at a time under a single context, so a
// broker whose address has gone dark spends the whole budget and the live
// ones are never asked. A deleted pod is such a broker until the
// controller drops it from metadata, about 14 s later. A liveness check
// that reads that as the cluster being unreachable starves the tick and
// makes the World read-only while two of three replicas serve (#129).
//
// A client that has discovered nothing yet, a fresh one, knows only its
// seeds, and so does the last resort when no discovered broker answers:
// kgo's Ping with what is left of ctx, which ends at the seeds. A seed
// cannot be asked directly; kgo refuses a request to its internal ID.
func Ping(ctx context.Context, cl *kgo.Client) error {
	brokers := cl.DiscoveredBrokers()
	if len(brokers) == 0 {
		return cl.Ping(ctx)
	}
	err := pingAll(ctx, brokers)
	if err == nil || ctx.Err() != nil {
		return err
	}
	return cl.Ping(ctx)
}

func pingAll(ctx context.Context, brokers []*kgo.Broker) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(brokers))
	for _, b := range brokers {
		go func() {
			req := kmsg.NewPtrMetadataRequest()
			req.Topics = []kmsg.MetadataRequestTopic{}
			_, err := b.Request(ctx, req)
			errs <- err
		}()
	}
	var err error
	for range brokers {
		if err = <-errs; err == nil {
			return nil
		}
	}
	return err
}
