// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Command content-conformance runs the AW-CLI-005 corpus against the
// AW-CLI-006 compiler (AC-1).
//
// It is the half of ADR-0009's compatibility mechanism that needs a compiler.
// `make content-grammar-check` checks the corpus against the grammar with lark
// and can run before any compiler exists; this checks the output, the errors,
// and the round trip against the real one.
//
// Exit 0 when the corpus and the compiler agree. Exit 1 with one line per
// disagreement, naming the pair.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/valesordev/andara/content/lang"
)

func main() {
	corpus := flag.String("corpus", "docs/specs/content-language/v1/corpus", "the conformance corpus")
	report := flag.String("report", "content-conformance.json", "where to write the machine-readable report; empty to skip")
	flag.Parse()

	rep, err := lang.Conformance(*corpus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "content-conformance: %v\n", err)
		os.Exit(2)
	}

	if *report != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "content-conformance: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(*report, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "content-conformance: %v\n", err)
			os.Exit(2)
		}
	}

	// A pending case is a specified construct waiting on a protobuf field, not
	// a failing case and not a skipped test (semantics.md §9). Printing the
	// count and the gating story per case is how it stays visible: the day the
	// field lands, the story that landed it moves the case into valid/.
	var pending []string
	for _, c := range rep.Cases {
		if c.Skipped {
			pending = append(pending, fmt.Sprintf("  %s — %s", c.Case, c.Gating))
		}
	}

	for _, c := range rep.Cases {
		if c.OK || c.Skipped {
			continue
		}
		for _, r := range c.Reasons {
			fmt.Fprintf(os.Stderr, "%s: %s\n", c.Case, strings.ReplaceAll(r, "\n", "\n  "))
		}
	}

	if len(pending) > 0 {
		sort.Strings(pending)
		fmt.Printf("content-conformance: %d pending case(s) skipped, each waiting on a protobuf field:\n", len(pending))
		fmt.Println(strings.Join(pending, "\n"))
	}

	if rep.Failed > 0 {
		fmt.Fprintf(os.Stderr, "\ncontent-conformance: %d case(s) disagree with the corpus\n", rep.Failed)
		os.Exit(1)
	}
	fmt.Printf("content-conformance: %d cases agree with the corpus\n", rep.Passed)
}
