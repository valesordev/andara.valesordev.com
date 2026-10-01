// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// AW-SRV-042 AC-1, the Gateway's half: while the World waits for its first
// content, OpenSession is refused UNAVAILABLE with ErrorInfo andara.game /
// no_content_in_effect and counted rejected_no_content, from 0; Admin is
// served as ever; and once the wait ends, OpenSession succeeds.
func TestOpenSession_RefusedWhileWaitingForContent(t *testing.T) {
	var waiting atomic.Bool
	waiting.Store(true)
	h := start(t, func(o *Options) { o.ContentWaiting = waiting.Load })
	counter := h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedNoContent)
	if got := testutil.ToFloat64(counter); got != 0 {
		t.Fatalf("rejected_no_content before any refusal = %v, want 0 (pre-seeded)", got)
	}

	client := h.game()
	_, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "test-token", ClientName: "waiting-test",
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("OpenSession while waiting: %v", err)
	}
	var info *errdetails.ErrorInfo
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if ei, ok := v.(*errdetails.ErrorInfo); ok {
				info = ei
			}
		}
	}
	if info == nil || info.GetDomain() != "andara.game" || info.GetReason() != "no_content_in_effect" {
		t.Fatalf("ErrorInfo = %v", info)
	}
	if got := testutil.ToFloat64(counter); got != 1 {
		t.Errorf("rejected_no_content = %v, want 1", got)
	}
	line := findLog(t, h.logs, "session rejected: no content in effect")
	if line["level"] != "DEBUG" || line["reason"] != "no_content_in_effect" || line["remote_addr"] == "" || line["trace_id"] == nil {
		t.Errorf("the refusal's line = %v", line)
	}

	// Admin is served while waiting.
	hc, _ := h.httpClient()
	admin := adminv1connect.NewAdminClient(hc, h.baseURL())
	req := connect.NewRequest(&adminv1.GetServerInfoRequest{})
	req.Header().Set("Authorization", "Bearer test-token")
	if _, err := admin.GetServerInfo(context.Background(), req); err != nil {
		t.Errorf("Admin while waiting: %v", err)
	}

	waiting.Store(false)
	h.open(t, client)
}
