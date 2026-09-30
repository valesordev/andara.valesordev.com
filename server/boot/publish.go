// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"fmt"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/recordlog"
)

// blobRecordHeadroom is what a blob record carries beyond its body: the
// contentv1.Blob envelope, its hash and media type.
const blobRecordHeadroom = 64 << 10

// openRegistry opens the content store's write side (AW-SRV-013) once: the
// three content topics, and the audit topic for the history they don't keep.
// It returns the audit log it read, for the boot's own records.
func (rt *Runtime) openRegistry(ctx context.Context) (recordlog.Log, error) {
	cfg := rt.Cfg
	var opened []recordlog.Log
	open := func(topic string, maxRecord int64) (recordlog.Log, error) {
		l, err := recordlog.NewKafka(ctx, recordlog.KafkaOptions{
			Brokers: cfg.KafkaBrokers, Topic: topic, ClientID: cfg.ServiceName + "-content",
			MaxRecordBytes: int32(maxRecord),
		})
		if err == nil {
			opened = append(opened, l)
		}
		return l, err
	}
	fail := func(err error) (recordlog.Log, error) {
		for _, l := range opened {
			_ = l.Close()
		}
		return nil, err
	}
	blobs, err := open(content.TopicBlobs, cfg.ContentMaxBlobBytes+blobRecordHeadroom)
	if err != nil {
		return fail(err)
	}
	versions, err := open(content.TopicVersions, 0)
	if err != nil {
		return fail(err)
	}
	active, err := open(content.TopicActive, 0)
	if err != nil {
		return fail(err)
	}
	var audit recordlog.Log
	switch cfg.AuthStore {
	case "memory":
		// Accounts aren't kept, and neither is their audit record: the
		// boot's records go to a log nothing will read back.
		audit = recordlog.NewMemory()
	default:
		audit, err = open(AuditTopic, 0)
		if err != nil {
			return fail(err)
		}
	}
	reg, err := content.OpenRegistry(ctx, content.RegistryOptions{
		Blobs: blobs, Versions: versions, Active: active, Audit: audit,
		Cache: content.BlobCache{Dir: cfg.ContentCacheDir},
	})
	if err != nil {
		return fail(err)
	}
	rt.registry = reg
	if rt.publishMetrics == nil {
		rt.publishMetrics = content.NewPublishMetrics(rt.Tel.Reg)
	}
	return audit, nil
}

// bootCore publishes the andara.core this build embeds and activates it
// (AW-SRV-013 AC-15 to AC-17), under the account store's single-writer
// assumption: one server replica.
func (rt *Runtime) bootCore(ctx context.Context) error {
	audit, err := rt.openRegistry(ctx)
	if err != nil {
		return err
	}
	if rt.Cfg.ContentCorePack != core.Pack {
		return fmt.Errorf("content.core_pack is %q, and this build embeds %s", rt.Cfg.ContentCorePack, core.Pack)
	}
	res, err := content.BootCore(ctx, content.CoreBootOptions{
		Registry: rt.registry,
		// The account store isn't open yet; its Auditor writes the same
		// topic the same way.
		Auditor: auth.NewAuditor(audit, rt.Tel.Log, nil, nil),
		Metrics: rt.publishMetrics,
		Log:     rt.Tel.Log,
		Tracer:  rt.Tel.Tracer,
		Pack:    rt.Cfg.ContentCorePack,
		Version: core.Version(),
		Blobs:   core.Blobs(),
		Build:   rt.BuildVersion,
	})
	if err != nil {
		return err
	}
	rt.core = &res
	return nil
}

// coreInEffect is boot rule 5: readiness waits until the World in effect
// includes andara.core at its active version. Nil when there's no core boot
// to wait for (content.source=dir, --validate-only).
func (rt *Runtime) coreInEffect() error {
	if rt.core == nil || rt.Content == nil {
		return nil
	}
	pack := rt.Cfg.ContentCorePack
	if got, ok := rt.Content.Versions()[pack]; !ok || got != rt.core.Active {
		return fmt.Errorf("%s@%d is active, and the World in effect has %s@%d: this build cannot load its active core",
			pack, rt.core.Active, pack, got)
	}
	return nil
}

// OpenContentAdmin builds the content publish path (AW-SRV-013) over the
// registry LoadContent opened. It returns nil on a content.source=dir server,
// which has no store to publish into: those RPCs stay UNIMPLEMENTED there.
// Call it after OpenAccounts.
func (rt *Runtime) OpenContentAdmin(ctx context.Context) (*content.Admin, *content.Registry, error) {
	cfg := rt.Cfg
	if rt.Content == nil || rt.Content.Loader() == nil {
		return nil, nil, nil
	}
	if rt.Accounts == nil {
		return nil, nil, errors.New("content publishing needs the account store")
	}
	if rt.registry == nil {
		if _, err := rt.openRegistry(ctx); err != nil {
			return nil, nil, err
		}
	}
	admin, err := content.NewAdmin(content.AdminOptions{
		Registry:             rt.registry,
		Loader:               rt.Content.Loader(),
		Blobs:                rt.Content.Resolver(),
		Accounts:             rt.Accounts,
		Auditor:              rt.Accounts.Auditor(),
		Metrics:              rt.publishMetrics,
		Log:                  rt.Tel.Log,
		Tracer:               rt.Tel.Tracer,
		MaxBlobBytes:         cfg.ContentMaxBlobBytes,
		MaxPackBytes:         cfg.ContentMaxPackBytes,
		CorePack:             cfg.ContentCorePack,
		OperatorSelfApproval: cfg.ContentOperatorSelfApproval,
		Reload: func(ctx context.Context) (map[string]uint64, error) {
			_, err := rt.Content.Reconcile(ctx)
			return rt.Content.Versions(), err
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return admin, rt.registry, nil
}
