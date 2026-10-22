// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package lifecycle is the pre-stop half of a deploy (AW-SRV-054,
// docs/specs/deploy/lifecycle.md): the Lifecycle service the running server
// serves on deploy.control_socket, and the client `andara-server prestop` is.
//
// PrepareStop announces the stop to every open stream, waits the notice lead,
// refuses Submit with server_restarting, waits for what was admitted, takes a
// Snapshot Round at the next tick boundary, tags it `deploy:<image tag>`, and
// waits for its offsets to be committed. It does it once: a second call gets
// the first call's response.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/tickloop"
)

// Announcer writes ServerStopping to every open Subscribe stream: the egress.
type Announcer interface {
	AnnounceStopping(message string, expectedBack time.Duration) int
}

// Refuser is the draining flag: the ingress.
type Refuser interface {
	RefuseRestarting()
	WaitAdmitted(ctx context.Context) bool
}

// Rounds takes the pre-stop round: the tick loop's snapshotter.
type Rounds interface {
	Force() <-chan tickloop.ForcedRound
	CancelForce(<-chan tickloop.ForcedRound) (withdrawn bool)
}

// Options is everything PrepareStop reads.
type Options struct {
	Announcer Announcer
	Refuser   Refuser
	Rounds    Rounds
	// Store is where the round was written, for its tag; nil fails the tag
	// as a store error.
	Store sim.WorldStore
	// Committed is the tick of the newest checkpoint committed; PrepareStop
	// waits for it to reach the round's tick. Nil skips the wait.
	Committed func() sim.Tick

	// Message is ServerStopping.message: empty until Brian rules (the
	// story's open question).
	Message       string
	NoticeLead    time.Duration // deploy.notice_lead
	AdmitWait     time.Duration // deploy.admit_wait
	ExpectedBack  time.Duration // deploy.expected_back
	ImageTag      string        // deploy.image_tag
	UploadTimeout time.Duration // snapshot.upload_timeout
	// TickInterval bounds the wait for the next boundary and for the
	// checkpoint after it: two ticks each.
	TickInterval time.Duration

	Log    *slog.Logger
	Tracer trace.Tracer
	// Now and Sleep are the clock, for tests. Nil means the real one.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

// Service implements adminv1connect.LifecycleHandler.
type Service struct {
	o Options

	mu   sync.Mutex
	done chan struct{} // closed when the first call's response is ready
	resp *adminv1.PrepareStopResponse
}

var _ adminv1connect.LifecycleHandler = (*Service)(nil)

// New builds the service.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
			}
		}
	}
	return &Service{o: o}
}

// TagFor is the round tag pre-stop writes for an image tag.
func TagFor(imageTag string) string { return store.DeployTagPrefix + imageTag }

// DeployID is the deploy's correlation id, `<image tag>@<tick>` (ruling 5).
func DeployID(imageTag string, tick sim.Tick) string {
	return fmt.Sprintf("%s@%d", imageTag, tick)
}

// PrepareStop implements adminv1connect.LifecycleHandler. The sequence runs
// once, on its own context: a caller that goes away does not stop a stop
// half-way, and a second call waits for the first's response and returns it.
func (s *Service) PrepareStop(ctx context.Context, _ *connect.Request[adminv1.PrepareStopRequest]) (*connect.Response[adminv1.PrepareStopResponse], error) {
	s.mu.Lock()
	first := s.done == nil
	if first {
		s.done = make(chan struct{})
	}
	done := s.done
	s.mu.Unlock()
	if first {
		func() {
			// Closed even if the sequence panics, so a second call cannot
			// wait on it forever; it then finds no response.
			defer close(done)
			resp := s.run(context.WithoutCancel(ctx))
			s.mu.Lock()
			s.resp = resp
			s.mu.Unlock()
		}()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeDeadlineExceeded, ctx.Err())
	}
	s.mu.Lock()
	resp := s.resp
	s.mu.Unlock()
	if resp == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the pre-stop sequence did not finish"))
	}
	return connect.NewResponse(resp), nil
}

// run is the sequence (lifecycle.md, Sequence).
func (s *Service) run(ctx context.Context) *adminv1.PrepareStopResponse {
	o := s.o
	start := o.Now()
	streams := o.Announcer.AnnounceStopping(o.Message, o.ExpectedBack)
	o.Log.LogAttrs(ctx, slog.LevelInfo, "prestop: server stopping announced",
		slog.Int("streams", streams), slog.String("notice_lead", o.NoticeLead.String()))
	// The World keeps running and accepting Submit through the lead.
	o.Sleep(ctx, o.NoticeLead)
	o.Refuser.RefuseRestarting()
	refusedAt := o.Now()
	actx, cancel := context.WithTimeout(ctx, o.AdmitWait)
	if !o.Refuser.WaitAdmitted(actx) {
		o.Log.LogAttrs(ctx, slog.LevelWarn, "prestop: admitted Submits still in flight after deploy.admit_wait; the log decides them",
			slog.String("admit_wait", o.AdmitWait.String()))
	}
	cancel()

	// The span is the round's: the boundary, the Puts, the tag and the
	// commit. Its trace is the one `prestop complete` names.
	sctx, span := o.Tracer.Start(ctx, "deploy.prestop_snapshot", trace.WithAttributes(attribute.String("image_tag", o.ImageTag)))
	defer span.End()
	resp, cause := s.snapshot(sctx)
	if resp.Outcome == adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE {
		span.SetAttributes(attribute.String("deploy_id", resp.DeployId), attribute.Int64("tick", int64(resp.Tick)))
	} else {
		span.SetStatus(codes.Error, cause.Error())
	}
	span.SetAttributes(attribute.String("outcome", outcomeName(resp.Outcome)))
	now := o.Now()
	resp.DurationMs = uint32(now.Sub(start) / time.Millisecond)
	resp.RefusedMs = uint32(now.Sub(refusedAt) / time.Millisecond)
	traceID := ""
	if sc := span.SpanContext(); sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if cause != nil {
		o.Log.LogAttrs(sctx, slog.LevelError, "prestop snapshot failed: the previous complete round is the recovery point",
			slog.String("outcome", outcomeName(resp.Outcome)), slog.String("cause", cause.Error()), slog.String("trace_id", traceID))
	}
	o.Log.LogAttrs(sctx, slog.LevelInfo, "prestop complete",
		slog.Uint64("tick", resp.Tick),
		slog.String("hash", fmt.Sprintf("%x", resp.StateHash)),
		slog.String("tag", resp.Tag),
		slog.Uint64("duration_ms", uint64(resp.DurationMs)),
		slog.Uint64("refused_ms", uint64(resp.RefusedMs)),
		slog.String("outcome", outcomeName(resp.Outcome)),
		slog.String("deploy_id", resp.DeployId),
		slog.String("trace_id", traceID))
	return resp
}

// snapshot takes the round at the next boundary and tags it, inside
// snapshot.upload_timeout plus two ticks for the boundary, then waits at most
// two ticks and a second for its checkpoint. Anything but COMPLETE carries
// its cause, and names no tagged round.
func (s *Service) snapshot(ctx context.Context) (*adminv1.PrepareStopResponse, error) {
	o := s.o
	wctx, cancel := context.WithTimeout(ctx, o.UploadTimeout+2*o.TickInterval)
	defer cancel()
	var round tickloop.ForcedRound
	ch := o.Rounds.Force()
	select {
	case round = <-ch:
	case <-wctx.Done():
		// Withdrawn, so no round is taken later for a pre-stop that reported
		// TIMEOUT: the previous complete round stays the recovery point.
		o.Rounds.CancelForce(ch)
		return failed(adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT), fmt.Errorf("the round did not complete within snapshot.upload_timeout: %w", wctx.Err())
	}
	// The tag shares the round's budget: snapshot.upload_timeout is one term
	// of the grace bound and of `prestop`'s, not one per step. A tag cut
	// short leaves some objects tagged, which names no round (RoundTags), so
	// the TIMEOUT it reports holds.
	if round.Err != nil {
		outcome := adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR
		if errors.Is(round.Err, context.DeadlineExceeded) {
			outcome = adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT
		}
		return failed(outcome), fmt.Errorf("round at tick %d: %w", round.Tick, round.Err)
	}
	tag := TagFor(o.ImageTag)
	if o.Store == nil {
		return failed(adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR), errors.New("no snapshot store to tag the round in")
	}
	if err := store.TagRound(wctx, o.Store, round.Keys, tag); err != nil {
		outcome := adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT
		}
		return failed(outcome), fmt.Errorf("tag the round at tick %d: %w", round.Tick, err)
	}
	s.awaitCommit(ctx, round.Tick)
	hash := round.Hash
	return &adminv1.PrepareStopResponse{
		Outcome:   adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE,
		Tick:      uint64(round.Tick),
		StateHash: hash[:],
		Tag:       tag,
		DeployId:  DeployID(o.ImageTag, round.Tick),
	}, nil
}

// awaitCommit waits, inside ctx, for the loop to commit the round's offsets:
// the loop checkpoints the round's tick once its boundary is delivered. A
// commit that doesn't come is logged and not a failure: the round is
// complete and is the recovery point either way.
func (s *Service) awaitCommit(ctx context.Context, tick sim.Tick) {
	o := s.o
	if o.Committed == nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 2*o.TickInterval+time.Second)
	defer cancel()
	for o.Committed() < tick {
		if wctx.Err() != nil {
			o.Log.LogAttrs(ctx, slog.LevelWarn, "prestop: the round's offsets were not committed yet; the next boot replays from the round",
				slog.Uint64("tick", uint64(tick)), slog.Uint64("committed", uint64(o.Committed())),
				slog.String("trace_id", trace.SpanContextFromContext(ctx).TraceID().String()))
			return
		}
		o.Sleep(wctx, 10*time.Millisecond)
	}
}

func failed(outcome adminv1.PrepareStopOutcome) *adminv1.PrepareStopResponse {
	return &adminv1.PrepareStopResponse{Outcome: outcome}
}

// outcomeName is the `outcome` field: complete, timeout, store_error.
func outcomeName(o adminv1.PrepareStopOutcome) string {
	switch o {
	case adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE:
		return "complete"
	case adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_TIMEOUT:
		return "timeout"
	case adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR:
		return "store_error"
	}
	return "unspecified"
}
