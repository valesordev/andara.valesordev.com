// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package kafkatest is the integration suite's Kafka client configuration:
// its brokers and, when they are exported, the suite principal's credentials
// (AW-SRV-044). `make test-integration` exports andara-it's username and
// password file; unset, the suite connects without SASL, as against a broker
// that requires none.
package kafkatest

import (
	"os"

	"github.com/valesordev/andara/server/kafkaclient"
)

// Config is brokers with the credentials from ANDARA_KAFKA_SASL_USERNAME and
// ANDARA_KAFKA_SASL_PASSWORD_FILE. A site names its client with For.
func Config(brokers []string) kafkaclient.Config {
	return kafkaclient.Config{
		Brokers:          brokers,
		SASLUsername:     os.Getenv("ANDARA_KAFKA_SASL_USERNAME"),
		SASLPasswordFile: os.Getenv("ANDARA_KAFKA_SASL_PASSWORD_FILE"),
	}
}
