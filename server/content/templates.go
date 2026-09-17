// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// TemplatesSubdir is where a pack keeps its Template blobs, one per
// declaration: `templates/<name>.json`. The pack-path convention AW-CLI-006
// emits and AW-SRV-012 reads back; in dir mode it is a subdirectory of
// content.path.
const TemplatesSubdir = "templates"

// LoadTemplatesDir reads `<dir>/templates/*.json`, one TemplateDefinition
// per file, in sorted name order. A content directory with no templates
// subdirectory has no Templates, which is legal: a World of Rooms with
// nothing in them is still a World.
func LoadTemplatesDir(dir string) ([]sim.TemplateInput, []sim.ValidationError) {
	tdir := filepath.Join(dir, TemplatesSubdir)
	entries, err := os.ReadDir(tdir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []sim.ValidationError{{File: tdir, Code: sim.ErrMalformed,
			Detail: fmt.Sprintf("cannot read %s: %s", tdir, err.Error())}}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var (
		inputs []sim.TemplateInput
		errs   []sim.ValidationError
	)
	for _, name := range names {
		path := filepath.Join(tdir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, sim.ValidationError{File: path, Code: sim.ErrMalformed, Detail: err.Error()})
			continue
		}
		def, verr := parseTemplateJSON(path, data)
		if verr != nil {
			errs = append(errs, *verr)
			continue
		}
		inputs = append(inputs, sim.TemplateInput{File: path, Def: def})
	}
	return inputs, errs
}

// LoadTemplates returns TemplateDefinitions from the configured source, the
// way Load does for Zones. Kafka is AW-SRV-012.
func LoadTemplates(source, path string) ([]sim.TemplateInput, []sim.ValidationError) {
	switch source {
	case SourceDir:
		return LoadTemplatesDir(path)
	case SourceKafka:
		return nil, []sim.ValidationError{{Code: sim.ErrEmptyContent,
			Detail: "no Templates were found in kafka: content.source=kafka is not implemented (AW-SRV-012); set content.source=dir"}}
	default:
		return nil, []sim.ValidationError{{Code: sim.ErrMalformed,
			Detail: `content.source "` + source + `" is not kafka or dir`}}
	}
}

func parseTemplateJSON(path string, data []byte) (*contentv1.TemplateDefinition, *sim.ValidationError) {
	if err := json.Unmarshal(data, new(map[string]any)); err != nil {
		line := 0
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line = lineFromOffset(data, se.Offset)
		}
		return nil, &sim.ValidationError{File: path, Line: line, Code: sim.ErrMalformed, Detail: err.Error()}
	}
	def := &contentv1.TemplateDefinition{}
	u := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := u.Unmarshal(data, def); err != nil {
		return nil, &sim.ValidationError{File: path, Line: protojsonLine(err.Error()), Code: sim.ErrMalformed, Detail: err.Error()}
	}
	return def, nil
}
