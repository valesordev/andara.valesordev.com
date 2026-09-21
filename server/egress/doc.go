// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package egress is the Session's side of the Event stream (AW-SRV-011):
// what Game.Subscribe does between the fan-out's per-subscriber buffer and
// the client's socket.
//
// It implements gateway.Egress. Each Session that subscribes gets one
// events.Hub subscription for as long as the Session lives, read by its
// own goroutine into a bounded ring of what the Session has been sent —
// egress.resume_window Events — so a stream that ends and is reopened with
// last_event_id resumes from the next Event with no gap and no duplicate,
// or is told with a Resync frame that it cannot. The stream is a cursor
// over that ring: a client that stops reading leaves the cursor behind,
// and when it trails by more than egress.buffer the stream is ended with
// a typed reason rather than the Session's memory growing or an Event
// being skipped. A stream that has nothing to send for
// egress.heartbeat_interval sends a Heartbeat so a quiet World and a dead
// connection look different.
//
// Nothing here runs on the tick: the Hub's Publish is one enqueue, the
// Hub's goroutine fills each subscription's buffer, and this package's
// per-Session goroutine drains it. A stalled client costs its own stream
// and nothing else (AC-3, AC-4).
package egress
