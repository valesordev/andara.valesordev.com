package content

import (
	"bytes"
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

// LoadDir reads a directory of Zone Definition JSON files (one Zone per file).
// Non-JSON names are ignored. File names are sorted so load order is stable.
func LoadDir(dir string) ([]sim.Input, []sim.ValidationError) {
	source := "dir:" + dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		code := sim.ErrEmptyContent
		detail := "no Zones were found in " + source
		if !errors.Is(err, os.ErrNotExist) {
			code = sim.ErrMalformed
			detail = fmt.Sprintf("cannot read %s: %s", dir, err.Error())
		}
		return nil, []sim.ValidationError{{
			File:   dir,
			Code:   code,
			Detail: detail,
		}}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, []sim.ValidationError{{
			File:   dir,
			Code:   sim.ErrEmptyContent,
			Detail: "no Zones were found in " + source,
		}}
	}

	var (
		inputs []sim.Input
		errs   []sim.ValidationError
	)
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, sim.ValidationError{
				File:   path,
				Code:   sim.ErrMalformed,
				Detail: err.Error(),
			})
			continue
		}
		def, verr := parseZoneJSON(path, data)
		if verr != nil {
			errs = append(errs, *verr)
			continue
		}
		inputs = append(inputs, sim.Input{File: path, Def: def})
	}
	if len(inputs) == 0 && len(errs) == 0 {
		return nil, []sim.ValidationError{{
			File:   dir,
			Code:   sim.ErrEmptyContent,
			Detail: "no Zones were found in " + source,
		}}
	}
	return inputs, errs
}

func parseZoneJSON(path string, data []byte) (*contentv1.ZoneDefinition, *sim.ValidationError) {
	if err := json.Unmarshal(data, new(map[string]any)); err != nil {
		line := 0
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line = lineFromOffset(data, se.Offset)
		}
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			line = lineFromOffset(data, te.Offset)
		}
		return nil, &sim.ValidationError{
			File:   path,
			Line:   line,
			Code:   sim.ErrMalformed,
			Detail: err.Error(),
		}
	}
	def := &contentv1.ZoneDefinition{}
	u := protojson.UnmarshalOptions{DiscardUnknown: false}
	if err := u.Unmarshal(data, def); err != nil {
		return nil, &sim.ValidationError{
			File:   path,
			Code:   sim.ErrMalformed,
			Detail: err.Error(),
		}
	}
	return def, nil
}

func lineFromOffset(data []byte, offset int64) int {
	if offset <= 0 {
		return 1
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	// Offset is a 1-based index into the input; slice up to the error byte.
	end := int(offset)
	if end > len(data) {
		end = len(data)
	}
	if end < 1 {
		return 1
	}
	return bytes.Count(data[:end], []byte{'\n'}) + 1
}
