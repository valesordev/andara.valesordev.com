// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/config"
)

// topicTable is every topic the server and the projector use, by method.
func topicTable(t Topics) map[string]string {
	return map[string]string{
		"andara.commands.v1":         t.Commands(),
		"andara.events.v1":           t.Events(),
		"andara.state.v1":            t.State(),
		"andara.accounts.v1":         t.Accounts(),
		"andara.audit.v1":            t.Audit(),
		"andara.content.blobs.v1":    t.ContentBlobs(),
		"andara.content.versions.v1": t.ContentVersions(),
		"andara.content.active.v1":   t.ContentActive(),
	}
}

// AC-1: with prefix dev every name is "dev." and the unprefixed name.
func TestTopics_DevPrefixesEveryTopic(t *testing.T) {
	dev, err := NewTopics("dev")
	if err != nil {
		t.Fatal(err)
	}
	for base, got := range topicTable(dev) {
		if want := "dev." + base; got != want {
			t.Errorf("%s: got %q, want %q", base, got, want)
		}
	}
}

// AC-3: no prefix is today's names, byte for byte.
func TestTopics_EmptyPrefixIsTheUnprefixedNames(t *testing.T) {
	none, err := NewTopics("")
	if err != nil {
		t.Fatal(err)
	}
	for base, got := range topicTable(none) {
		if got != base {
			t.Errorf("got %q, want %q", got, base)
		}
	}
	if got := (Topics{}).Commands(); got != "andara.commands.v1" {
		t.Errorf("zero Topics: commands = %q", got)
	}
}

// AC-5a: TopicsFor resolves the configured prefix onto every topic.
func TestTopicsFor_PrefixesEveryTopicFromConfig(t *testing.T) {
	tp, err := TopicsFor(config.Config{KafkaTopicPrefix: "it7"})
	if err != nil {
		t.Fatal(err)
	}
	if tp.Prefix() != "it7" {
		t.Errorf("prefix = %q", tp.Prefix())
	}
	for base, got := range topicTable(tp) {
		if want := "it7." + base; got != want {
			t.Errorf("%s: got %q, want %q", base, got, want)
		}
	}
}

// AC-8: a prefix that is not empty and not [a-z0-9]+ is refused, naming the key.
func TestNewTopics_RefusesAMalformedPrefix(t *testing.T) {
	for _, p := range []string{"dev.", "Dev", "dev_1", "dev-1", ".dev", "dé", " dev", "dev local"} {
		if _, err := NewTopics(p); err == nil || !strings.Contains(err.Error(), "kafka.topic_prefix") {
			t.Errorf("NewTopics(%q): err = %v, want one naming kafka.topic_prefix", p, err)
		}
		if _, err := TopicsFor(config.Config{KafkaTopicPrefix: p}); err == nil {
			t.Errorf("TopicsFor(%q) resolved", p)
		}
	}
}

// topicLiteral is a whole topic name as a Go string literal, closing quote
// included, so "andara.admin.v1.Admin" and a span name do not match (AC-5).
var topicLiteral = regexp.MustCompile(`"andara\.[a-z][a-z.]*\.v[0-9]+"`)

// topicLiterals is where src's string literals are whole topic names.
func topicLiterals(fset *token.FileSet, f *ast.File) []token.Position {
	var out []token.Position
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		// A raw string is held to the same rule: quote it as Go would.
		if v, err := strconv.Unquote(lit.Value); err == nil && topicLiteral.MatchString(strconv.Quote(v)) {
			out = append(out, fset.Position(lit.Pos()))
		}
		return true
	})
	return out
}

func TestTopicLiterals_MatchesWholeNamesOnly(t *testing.T) {
	src := `package p
const a = "andara.content.blobs.v1"
const b = "andara.admin.v1.Admin"
const c = "andara.events.v1"
const d = "Game/Submit"
// "andara.state.v1" in a comment is not a literal.
const e = ` + "`andara.audit.v1`" + `
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	for _, p := range topicLiterals(fset, f) {
		lines = append(lines, p.Line)
	}
	if fmt.Sprint(lines) != "[2 4 7]" {
		t.Errorf("matched lines %v, want [2 4 7]", lines)
	}
}

// sources parses every non-test Go file under server/, cmd/, internal/ and
// admin/ (gen/ is outside them).
func sources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, dir := range []string{"server", "cmd", "internal", "admin"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			files[filepath.ToSlash(rel)] = f
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) < 50 {
		t.Fatalf("found %d source files; is the walk rooted at the repository?", len(files))
	}
	return fset, files
}

const builderFile = "server/kafkaclient/topics.go"

// AC-5: a whole topic name appears only in the builder's table, and the old
// exported constants are gone.
func TestTopicNames_OnlyTheBuilderSpellsThem(t *testing.T) {
	fset, files := sources(t)
	old := map[string]bool{
		"CommandsTopic": true, "EventsTopic": true, "StateTopic": true, "AccountsTopic": true,
		"AuditTopic": true, "TopicBlobs": true, "TopicVersions": true, "TopicActive": true,
	}
	found := 0
	for path, f := range files {
		for _, p := range topicLiterals(fset, f) {
			if path == builderFile {
				found++
				continue
			}
			t.Errorf("%s:%d: a topic name outside the builder; use kafkaclient.Topics", path, p.Line)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if old[n.Name] {
						t.Errorf("%s:%d: %s is declared again; topics are named by kafkaclient.Topics", path, fset.Position(n.Pos()).Line, n.Name)
					}
				}
			}
		}
	}
	if found != len(topicTable(Topics{})) {
		t.Errorf("the builder spells %d topic names, its table has %d", found, len(topicTable(Topics{})))
	}
}

// AC-5a: NewTopics is called by TopicsFor and nowhere else.
func TestNewTopics_OnlyTopicsForCallsIt(t *testing.T) {
	fset, files := sources(t)
	calls := 0
	for path, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok2 := n.(*ast.CallExpr)
				if !ok2 {
					return true
				}
				var name string
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name != "NewTopics" {
					return true
				}
				if ok && path == builderFile && fd.Recv == nil && fd.Name.Name == "TopicsFor" {
					calls++
					return true
				}
				t.Errorf("%s:%d: NewTopics called outside TopicsFor", path, fset.Position(call.Pos()).Line)
				return true
			})
		}
	}
	if calls != 1 {
		t.Errorf("TopicsFor calls NewTopics %d times, want 1", calls)
	}
}
