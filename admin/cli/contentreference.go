// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// referenceFormatVersion is the shape's version: it moves only for a breaking
// change, and a new entry in any list is not one (AW-CLI-009).
const referenceFormatVersion = 1

// reference is what `content reference` prints: the Builder's reference,
// from what this binary carries and nothing it fetches (AW-CLI-009).
type reference struct {
	FormatVersion  int                  `json:"format_version"`
	Directions     []referenceDirection `json:"directions"`
	ComponentTypes []referenceComponent `json:"component_types"`
	Core           referenceCore        `json:"core"`
	Diagnostics    []referenceDiag      `json:"diagnostics"`
}

type referenceDirection struct {
	Name    string `json:"name"`
	Reverse string `json:"reverse"`
}

type referenceComponent struct {
	Type   string           `json:"type"`
	Fields []referenceField `json:"fields"`
}

type referenceField struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type referenceCore struct {
	Pack      string              `json:"pack"`
	Version   uint64              `json:"version"`
	Templates []referenceTemplate `json:"templates"`
}

type referenceTemplate struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Chain []string `json:"chain"`
}

type referenceDiag struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	RaisedBy string `json:"raised_by"`
}

func newContentReferenceCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "reference",
		Short: "Print the Directions, Component types, core Templates and diagnostic codes this binary enforces",
		Long: "Print the Builder's reference from what this andara-cli carries: the Direction set\n" +
			"with reverses, the Component types with their fields, the andara.core it embeds\n" +
			"(`andara-cli version`) with each Template's chain, and every diagnostic code with\n" +
			"its severity and whether the compiler, the loader, or both raise it.\n\n" +
			"Offline: it dials nothing and reads no content cache. Under --output json, stdout\n" +
			"is one object and nothing else.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ref, err := buildReference()
			if err != nil {
				return err
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(ref)
			}
			return rt.writeReferenceHuman(ref)
		},
	}
}

// buildReference assembles the four sections, each in the order the contract
// names: Directions in canonical compass order, every other list sorted by
// its key.
func buildReference() (reference, error) {
	ref := reference{FormatVersion: referenceFormatVersion}
	for _, d := range sim.Directions() {
		rev, _ := d.Reverse()
		ref.Directions = append(ref.Directions, referenceDirection{Name: string(d), Reverse: string(rev)})
	}

	types := sim.ComponentTypes()
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	for _, t := range types {
		c := referenceComponent{Type: string(t), Fields: []referenceField{}}
		names := sim.ComponentFieldNames(t)
		sort.Strings(names)
		for _, f := range names {
			k, _ := sim.ComponentFieldKind(t, f)
			c.Fields = append(c.Fields, referenceField{Name: f, Kind: fieldKindName(k)})
		}
		ref.ComponentTypes = append(ref.ComponentTypes, c)
	}

	pack, err := embeddedCore()
	if err != nil {
		return reference{}, err
	}
	ref.Core = referenceCore{Pack: core.Pack, Version: core.Version(), Templates: []referenceTemplate{}}
	for _, t := range pack.Templates {
		ref.Core.Templates = append(ref.Core.Templates, referenceTemplate{
			Name: t.GetName(), Kind: templateKindName(t.GetKind()), Chain: append([]string{}, t.GetChain()...),
		})
	}
	sort.Slice(ref.Core.Templates, func(i, j int) bool { return ref.Core.Templates[i].Name < ref.Core.Templates[j].Name })

	for _, d := range lang.Diagnostics {
		ref.Diagnostics = append(ref.Diagnostics, referenceDiag{Code: d.Code, Severity: d.Severity, RaisedBy: d.RaisedBy})
	}
	sort.Slice(ref.Diagnostics, func(i, j int) bool { return ref.Diagnostics[i].Code < ref.Diagnostics[j].Code })
	return ref, nil
}

// fieldKindName spells a field's kind as the reference does.
func fieldKindName(k sim.FieldKind) string {
	switch k {
	case sim.FieldString:
		return "string"
	case sim.FieldInt:
		return "int"
	case sim.FieldBool:
		return "bool"
	}
	return "unknown"
}

// templateKindName spells a Template's kind as the source keyword.
func templateKindName(k contentv1.TemplateKind) string {
	switch k {
	case contentv1.TemplateKind_ENTITY:
		return "entity"
	case contentv1.TemplateKind_ITEM:
		return "item"
	case contentv1.TemplateKind_BEHAVIOR:
		return "behavior"
	}
	return "unknown"
}

// writeReferenceHuman prints the four sections as headed tables, with the
// same rows as the JSON.
func (rt *runtime) writeReferenceHuman(ref reference) error {
	tw := tabwriter.NewWriter(rt.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Directions")
	fmt.Fprintln(tw, "  DIRECTION\tREVERSE")
	for _, d := range ref.Directions {
		fmt.Fprintf(tw, "  %s\t%s\n", d.Name, d.Reverse)
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "Component types")
	fmt.Fprintln(tw, "  TYPE\tFIELDS")
	for _, c := range ref.ComponentTypes {
		fields := make([]string, 0, len(c.Fields))
		for _, f := range c.Fields {
			fields = append(fields, f.Name+":"+f.Kind)
		}
		shown := strings.Join(fields, ", ")
		if shown == "" {
			shown = "(marker)"
		}
		fmt.Fprintf(tw, "  %s\t%s\n", c.Type, shown)
	}
	fmt.Fprintln(tw)
	fmt.Fprintf(tw, "Core (%s@%d)\n", ref.Core.Pack, ref.Core.Version)
	fmt.Fprintln(tw, "  TEMPLATE\tKIND\tCHAIN")
	for _, t := range ref.Core.Templates {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", t.Name, t.Kind, strings.Join(t.Chain, " > "))
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "Diagnostics")
	fmt.Fprintln(tw, "  CODE\tSEVERITY\tRAISED BY")
	for _, d := range ref.Diagnostics {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", d.Code, d.Severity, d.RaisedBy)
	}
	return tw.Flush()
}
