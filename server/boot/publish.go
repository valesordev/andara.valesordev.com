// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"

	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/recordlog"
)

// blobRecordHeadroom is what a blob record carries beyond its body: the
// contentv1.Blob envelope, its hash and media type.
const blobRecordHeadroom = 64 << 10

// OpenContentAdmin builds the content publish path (AW-SRV-013) over the
// content store's topics. It returns nil on a content.source=dir server,
// which has no store to publish into: those RPCs stay UNIMPLEMENTED there.
// Call it after OpenAccounts and LoadContent.
func (rt *Runtime) OpenContentAdmin(ctx context.Context) (*content.Admin, *content.Registry, error) {
	cfg := rt.Cfg
	if rt.Content == nil || rt.Content.Loader() == nil {
		return nil, nil, nil
	}
	if rt.Accounts == nil {
		return nil, nil, errors.New("content publishing needs the account store")
	}
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
	closeAll := func() {
		for _, l := range opened {
			_ = l.Close()
		}
	}
	blobs, err := open(content.TopicBlobs, cfg.ContentMaxBlobBytes+blobRecordHeadroom)
	if err != nil {
		return nil, nil, err
	}
	versions, err := open(content.TopicVersions, 0)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	active, err := open(content.TopicActive, 0)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	reg, err := content.OpenRegistry(ctx, content.RegistryOptions{
		Blobs: blobs, Versions: versions, Active: active,
		Audit: rt.Accounts.AuditLog(),
		Cache: content.BlobCache{Dir: cfg.ContentCacheDir},
	})
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	admin, err := content.NewAdmin(content.AdminOptions{
		Registry:             reg,
		Loader:               rt.Content.Loader(),
		Blobs:                rt.Content.Resolver(),
		Accounts:             rt.Accounts,
		Auditor:              rt.Accounts.Auditor(),
		Metrics:              content.NewPublishMetrics(rt.Tel.Reg),
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
		_ = reg.Close()
		return nil, nil, err
	}
	return admin, reg, nil
}
