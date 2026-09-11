// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	outputHuman = "human"
	outputJSON  = "json"
)

type jsonErrorEnvelope struct {
	Error jsonErrorBody `json:"error"`
}

type jsonErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail"`
}

type logLine struct {
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Msg     string `json:"msg"`
	Command string `json:"command"`
	TraceID string `json:"trace_id"`
}

func (rt *runtime) writeJSON(v any) error {
	enc := json.NewEncoder(rt.stdout)
	return enc.Encode(v)
}

func (rt *runtime) writeError(err error) int {
	ae := classify(err)
	output := rt.peekedOutput
	if rt.settings != nil {
		output = rt.settings.Output
	}
	if output == outputJSON {
		if err := json.NewEncoder(rt.stdout).Encode(jsonErrorEnvelope{Error: jsonErrorBody{
			Code:    ae.Code,
			Message: ae.Message,
			Detail:  ae.Detail,
		}}); err != nil {
			fmt.Fprintln(rt.stderr, ae.Message)
		}
		return ae.Exit
	}
	fmt.Fprintln(rt.stderr, ae.Message)
	return ae.Exit
}

func peekOutput(args []string, lookup func(string) (string, bool)) string {
	out := outputHuman
	if v, ok := lookup("ANDARA_OUTPUT"); ok && v != "" {
		out = v
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--output" || a == "-o":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				out = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--output="):
			out = strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o="):
			out = strings.TrimPrefix(a, "-o=")
		}
	}
	return out
}

func levelEnabled(configured, incoming string) bool {
	rank := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
	cr, okc := rank[configured]
	ir, oki := rank[incoming]
	if !okc || !oki {
		return incoming == "error"
	}
	return ir >= cr
}
