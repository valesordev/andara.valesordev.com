// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"reflect"
	"testing"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// AW-SRV-054: ServerStopping renders the server's message when it carries
// one, and otherwise this client's own line from expected_back_seconds.
func TestRenderServerStopping(t *testing.T) {
	for _, c := range []struct {
		ss   *gamev1.ServerStopping
		want []string
	}{
		{&gamev1.ServerStopping{ExpectedBackSeconds: 60}, []string{"The server is restarting; it expects to be back in about 60 seconds. You will be reconnected."}},
		{&gamev1.ServerStopping{}, []string{"The server is restarting. You will be reconnected."}},
		{&gamev1.ServerStopping{Message: "The tide turns.", ExpectedBackSeconds: 60}, []string{"The tide turns."}},
	} {
		env := &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_ServerStopping{ServerStopping: c.ss}}
		if got := renderEvent(env); !reflect.DeepEqual(got, c.want) {
			t.Errorf("renderEvent(%v) = %q, want %q", c.ss, got, c.want)
		}
	}
}
