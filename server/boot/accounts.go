// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

// Topics the account store writes.
const (
	AccountsTopic = "andara.accounts.v1"
	AuditTopic    = "andara.audit.v1"
)

// OpenAccounts builds the account store from configuration: the keyring,
// the two record logs, the index replayed from the accounts topic, and the
// bootstrap operator if none exists. A failure here is a failed boot — a
// server that cannot authenticate anyone has nothing to serve.
func (rt *Runtime) OpenAccounts(ctx context.Context) (*auth.Store, error) {
	cfg := rt.Cfg
	ctx, span := rt.Tel.Tracer.Start(ctx, "accounts.load")
	defer span.End()

	keys, err := auth.LoadKeyring(cfg.AuthTokenKeyFile)
	if err != nil {
		return nil, err
	}
	limit, err := auth.ParseRateLimit(cfg.AuthRateLimit)
	if err != nil {
		return nil, err
	}

	var accounts, audit recordlog.Log
	switch cfg.AuthStore {
	case "memory":
		rt.Tel.Log.LogAttrs(ctx, slog.LevelWarn, "auth.store=memory: accounts are not persisted and will not survive a restart")
		accounts, audit = recordlog.NewMemory(), recordlog.NewMemory()
	case "kafka":
		accounts, err = recordlog.NewKafka(ctx, recordlog.KafkaOptions{Brokers: cfg.KafkaBrokers, Topic: AccountsTopic, ClientID: cfg.ServiceName})
		if err != nil {
			return nil, err
		}
		audit, err = recordlog.NewKafka(ctx, recordlog.KafkaOptions{Brokers: cfg.KafkaBrokers, Topic: AuditTopic, ClientID: cfg.ServiceName})
		if err != nil {
			_ = accounts.Close()
			return nil, err
		}
	default:
		return nil, fmt.Errorf("auth.store %q is not kafka or memory", cfg.AuthStore)
	}

	var workload auth.WorkloadVerifier
	if cfg.AuthK8sIssuer != "" {
		workload = auth.NewJWKSVerifier(cfg.AuthK8sIssuer, cfg.AuthK8sJWKSURL)
	}
	store, err := auth.Open(ctx, auth.Options{
		Accounts:   accounts,
		Audit:      audit,
		Keys:       keys,
		Argon2:     auth.Argon2Params{MemoryKiB: cfg.AuthArgon2MemoryKiB, Time: cfg.AuthArgon2Time, Threads: uint8(cfg.AuthArgon2Threads)},
		SessionTTL: cfg.AuthSessionTTL,
		RefreshTTL: cfg.AuthRefreshTTL,
		InviteTTL:  cfg.AuthInviteTTL,
		RateLimit:  limit,
		Workload:   workload,
		Characters: auth.CharacterOptions{MaxPerAccount: cfg.CharacterMaxPerAccount, NamePattern: cfg.CharacterNamePattern},
		Log:        rt.Tel.Log,
		Tracer:     rt.Tel.Tracer,
		Registry:   rt.Tel.Reg,
	})
	if err != nil {
		_ = accounts.Close()
		_ = audit.Close()
		return nil, err
	}
	if cfg.AuthBootstrapOperator != "" {
		username, password, ok := strings.Cut(cfg.AuthBootstrapOperator, ":")
		if !ok {
			_ = store.Close()
			return nil, fmt.Errorf("auth.bootstrap_operator must be username:password")
		}
		created, err := store.Bootstrap(ctx, username, password)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		if created {
			rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "bootstrap operator created; rotate its password with Admin.ResetPassword")
		} else {
			rt.Tel.Log.LogAttrs(ctx, slog.LevelInfo, "bootstrap operator ignored: an operator account already exists")
		}
	}
	rt.Accounts = store
	return store, nil
}
