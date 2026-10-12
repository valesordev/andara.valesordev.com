// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"github.com/valesordev/andara/server/config"
)

// The unprefixed topic names: the one table every topic the server and the
// projector use is named from (AW-SRV-058). prod keeps these names as they
// are; another environment on a shared broker reads them under its prefix.
const (
	baseCommands        = "andara.commands.v1"
	baseEvents          = "andara.events.v1"
	baseState           = "andara.state.v1"
	baseAccounts        = "andara.accounts.v1"
	baseAudit           = "andara.audit.v1"
	baseContentBlobs    = "andara.content.blobs.v1"
	baseContentVersions = "andara.content.versions.v1"
	baseContentActive   = "andara.content.active.v1"
)

// Topics names the topics of one environment: kafka.topic_prefix joined to
// each unprefixed name with a dot, or the unprefixed name when the prefix is
// empty (ADR-0013 decision 4). A process resolves it once, with TopicsFor, and
// passes it down; the zero value is the unprefixed names.
type Topics struct{ prefix string }

// NewTopics is the builder for prefix. A prefix that is not empty and not
// [a-z0-9]+ is an error naming kafka.topic_prefix. TopicsFor is its only
// caller outside tests.
func NewTopics(prefix string) (Topics, error) {
	if err := config.ValidateTopicPrefix(prefix); err != nil {
		return Topics{}, err
	}
	return Topics{prefix: prefix}, nil
}

// TopicsFor resolves the topic names cfg configures. The server's boot and
// the projector's main call it once per process.
func TopicsFor(cfg config.Config) (Topics, error) {
	return NewTopics(cfg.KafkaTopicPrefix)
}

// Prefix is kafka.topic_prefix as resolved; empty for unprefixed names.
func (t Topics) Prefix() string { return t.prefix }

// Name is base under the prefix: "dev" and "andara.events.v1" give
// "dev.andara.events.v1"; an empty prefix gives base.
func (t Topics) Name(base string) string {
	if t.prefix == "" {
		return base
	}
	return t.prefix + "." + base
}

// Commands is the Command log the tick loop consumes and ingress produces to.
func (t Topics) Commands() string { return t.Name(baseCommands) }

// Events is the Event log, tick boundaries included.
func (t Topics) Events() string { return t.Name(baseEvents) }

// State is the compacted current-state topic the projector writes.
func (t Topics) State() string { return t.Name(baseState) }

// Accounts is the Account record log.
func (t Topics) Accounts() string { return t.Name(baseAccounts) }

// Audit is the audit record log.
func (t Topics) Audit() string { return t.Name(baseAudit) }

// ContentBlobs is the content blob log, keyed by hash.
func (t Topics) ContentBlobs() string { return t.Name(baseContentBlobs) }

// ContentVersions is the content version manifest log.
func (t Topics) ContentVersions() string { return t.Name(baseContentVersions) }

// ContentActive is the content Active Pointer log.
func (t Topics) ContentActive() string { return t.Name(baseContentActive) }
