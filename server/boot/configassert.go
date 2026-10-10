// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/tickloop"
)

// dark is the broker address the check builds clients with: nothing listens
// on it, so the check reaches no broker.
var dark = []string{"127.0.0.1:1"}

// KafkaSites describes every Kafka client the server and the projector build,
// from the options their constructors build them with (AW-SRV-053).
func KafkaSites(cfg config.Config) []kafkaclient.Site {
	name := cfg.ServiceName
	var sites []kafkaclient.Site
	sites = append(sites, ingress.Sites(ingress.ProducerOptions{Brokers: dark, ClientID: name, Deadline: cfg.IngressProduceDeadline})...)
	sites = append(sites, tickloop.Sites(dark, name, tickloop.DeliveryTimeout)...)
	sites = append(sites, tickloop.ReaderSites(dark, recoveryClientID)...)
	sites = append(sites, tickloop.ReaderSites(dark, projector.ClientID)...)
	sites = append(sites, content.Sites(dark, content.DefaultClientID, content.Topics{})...)
	sites = append(sites, projector.Sites(dark)...)

	logs := []struct {
		name, topic, client string
		oneShot             kafkaclient.OneShot
		maxRecord           int32
	}{
		{"accounts", AccountsTopic, name, kafkaclient.AccountsScan, 0},
		{"audit", AuditTopic, name, kafkaclient.AuditReplay, 0},
		{"content blobs log", content.TopicBlobs, name + "-content", kafkaclient.ContentBlobsScan, 2 << 20},
		{"content versions log", content.TopicVersions, name + "-content", kafkaclient.ContentVersionsScan, 0},
		{"content active log", content.TopicActive, name + "-content", kafkaclient.ContentActiveWatch, 0},
	}
	for _, l := range logs {
		sites = append(sites,
			recordlog.ProducerSite(l.name+" producer", recordlog.KafkaOptions{Brokers: dark, Topic: l.topic, ClientID: l.client, MaxRecordBytes: l.maxRecord}),
			recordlog.ReplaySite(l.name+" replay", dark, l.topic, l.oneShot),
		)
	}
	return sites
}

// ConfigAssert is `andara-server config-assert` (AW-SRV-053): it reads back
// the settings of every site, prints each against the contract's value, and
// exits ExitFail when a setting the contract marks as failing deviates. A
// warning (compression) prints and exits ExitOK.
func ConfigAssert(sites []kafkaclient.Site, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	failed := 0
	for _, s := range sites {
		for _, f := range kafkaclient.Check(s) {
			attrs := []any{"client", f.Client, "setting", f.Setting, "value", f.Value, "contract", f.Contract}
			switch {
			case f.OK:
				_, _ = fmt.Fprintf(stdout, "ok    %s\n", f)
				log.Info("config-assert", attrs...)
			case f.Severity == kafkaclient.Warn:
				_, _ = fmt.Fprintf(stdout, "warn  %s\n", f)
				log.Warn("config-assert", attrs...)
			default:
				failed++
				_, _ = fmt.Fprintf(stdout, "FAIL  %s\n", f)
				log.Error("config-assert", attrs...)
			}
		}
	}
	if failed > 0 {
		_, _ = fmt.Fprintf(stderr, "config-assert: %d setting(s) deviate from docs/specs/kafka/client-contract.md\n", failed)
		return ExitFail
	}
	return ExitOK
}
