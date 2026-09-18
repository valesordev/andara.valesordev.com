// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command

import (
	"strconv"
	"strings"
	"unicode/utf8"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// DefaultMaxIntentBytes is command.max_intent_bytes when unset.
const DefaultMaxIntentBytes = 4096

// Intent is what a player typed, as the Gateway received it: untrusted,
// unparsed, and bound to the Session that sent it. Raw crosses no trust
// boundary until Parse has read it.
type Intent struct {
	SessionID string
	Raw       string
	// ClientRef is echoed on the resulting Events (SubmitRequest.client_ref).
	ClientRef string
}

// Parse binds an Intent to a LoggedCommand arm. It reads no World state and
// no Session state — only the verb table — and it is the first stage, so
// its rejections never consume a log offset.
//
// The returned Command carries only the arm: zone_id, actor_id, session_id,
// and the rest are the Gateway's to fill after authorize, from the binding.
// The second result is the resolved verb name, for the metric and the
// authorize stage.
//
// maxBytes is command.max_intent_bytes; zero means DefaultMaxIntentBytes.
// An Intent over it is rejected on its length alone, before anything
// proportional to that length is allocated (AC-12).
func Parse(in Intent, t *VerbTable, maxBytes int) (*logv1.LoggedCommand, string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxIntentBytes
	}
	if len(in.Raw) > maxBytes {
		return nil, "", &Error{Stage: StageParse, Code: CodeIntentTooLarge,
			Detail: "intent is " + strconv.Itoa(len(in.Raw)) + " bytes; the limit is " + strconv.Itoa(maxBytes)}
	}
	fields := strings.Fields(in.Raw)
	if len(fields) == 0 {
		return nil, "", &Error{Stage: StageParse, Code: CodeUnknownVerb, Detail: "nothing to do"}
	}
	verb, err := t.Resolve(strings.ToLower(fields[0]))
	if err != nil {
		return nil, "", err
	}
	values := make(map[string]string, len(verb.Bind)+len(verb.Args))
	for k, v := range verb.Bind {
		values[k] = v
	}
	// Positional arguments follow the verb; tokens past the last one are
	// ignored, which is what lets `look around` mean `look`.
	for i, spec := range verb.Args {
		if i+1 >= len(fields) {
			return nil, "", &Error{Stage: StageParse, Code: CodeMissingArgument, Arg: spec.Name,
				Detail: verb.Name + " needs a " + spec.Name}
		}
		val, err := checkArg(spec, strings.ToLower(fields[i+1]))
		if err != nil {
			return nil, "", &Error{Stage: StageParse, Code: CodeInvalidArgument, Arg: spec.Name, Detail: err.Error()}
		}
		values[spec.Name] = val
	}
	cmd := &logv1.LoggedCommand{SessionId: in.SessionID, ClientRef: in.ClientRef}
	switch verb.Kind {
	case sim.KindLook:
		cmd.Command = &logv1.LoggedCommand_Look{Look: &logv1.Look{}}
	case sim.KindMove:
		cmd.Command = &logv1.LoggedCommand_Move{Move: &logv1.Move{Direction: values["direction"]}}
	}
	return cmd, verb.Name, nil
}

// quote renders a player's token for a message they will read back:
// quoted, escaped, and cut at 32 runes so a rejection is never longer
// than the Intent that earned it was allowed to be.
func quote(s string) string {
	if utf8.RuneCountInString(s) > 32 {
		r := []rune(s)
		s = string(r[:32]) + "…"
	}
	return strconv.Quote(s)
}
