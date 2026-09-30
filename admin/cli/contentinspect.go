// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/valesordev/andara/content/lang"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// `content inspect` prints a resolved definition: what a Zone, a Room, or a
// Template is once compiled, which is the answer to "why does this Template
// have that value" when the value is in none of the files the Builder wrote
// (ADR-0010 §9). It reads the same validated pack `content validate` builds,
// so what it shows is what the server would load.

func newContentInspectCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "inspect",
		Short:         "Print a resolved Zone, Room, or Template",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newInspectCmd(rt, "zone <zone>", "Print a Zone's summary: its fallback, Rooms, and Exits", rt.inspectZone))
	cmd.AddCommand(newInspectCmd(rt, "room <zone>/<room>", "Print a Room with its Components, and its Exits in Direction order", rt.inspectRoom))
	cmd.AddCommand(newInspectCmd(rt, "template <pack>.<Name>", "Print a flattened Template, naming the ancestor that set each field", rt.inspectTemplate))
	return cmd
}

func newInspectCmd(rt *runtime, use, short string, run func(*validated, string) error) *cobra.Command {
	var f packFlags
	cmd := &cobra.Command{
		Use:           use,
		Short:         short,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := rt.load(&f)
			if err != nil {
				return err
			}
			if v.failed() {
				// Nothing resolved to inspect. The findings are the answer.
				return rt.reportValidated(v)
			}
			return run(v, args[0])
		},
	}
	f.register(cmd)
	return cmd
}

func notFound(what, ref string) error {
	return &AppError{Exit: ExitFail, Code: "not_found", Message: fmt.Sprintf("no %s %s in this pack", what, ref), Detail: map[string]any{"ref": ref}}
}

// --- zone --------------------------------------------------------------------

func (v *validated) zone(id string) *contentv1.ZoneDefinition {
	for _, z := range v.zones() {
		if z.GetId() == id {
			return z
		}
	}
	return nil
}

func (rt *runtime) inspectZone(v *validated, id string) error {
	z := v.zone(id)
	if z == nil {
		return notFound("Zone", id)
	}
	exits, leaving := 0, 0
	type roomRow struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Exits int    `json:"exits"`
	}
	rows := make([]roomRow, 0, len(z.GetRooms()))
	for _, r := range z.GetRooms() {
		rows = append(rows, roomRow{ID: r.GetId(), Title: r.GetTitle(), Exits: len(r.GetExits())})
		for _, e := range r.GetExits() {
			exits++
			if e.GetToZone() != "" && e.GetToZone() != z.GetId() {
				leaving++
			}
		}
	}
	comps := componentTypes(z.GetComponents())
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(struct {
			ID         string    `json:"id"`
			Name       string    `json:"name"`
			Fallback   string    `json:"fallback"`
			Components []string  `json:"components"`
			Exits      int       `json:"exits"`
			Leaving    int       `json:"exits_leaving_zone"`
			Rooms      []roomRow `json:"rooms"`
		}{z.GetId(), z.GetName(), z.GetFallbackRoom(), comps, exits, leaving, rows})
	}
	tw := tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "zone:\t%s\n", z.GetId())
	fmt.Fprintf(tw, "name:\t%s\n", z.GetName())
	fmt.Fprintf(tw, "fallback:\t%s\n", z.GetFallbackRoom())
	fmt.Fprintf(tw, "components:\t%s\n", orNone(strings.Join(comps, ", ")))
	fmt.Fprintf(tw, "rooms:\t%d\n", len(rows))
	fmt.Fprintf(tw, "exits:\t%d, %d leaving the Zone\n", exits, leaving)
	if err := tw.Flush(); err != nil {
		return err
	}
	tw = tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", r.ID, lang.Quote(r.Title), plural(r.Exits, "exit"))
	}
	return tw.Flush()
}

// --- room --------------------------------------------------------------------

func (rt *runtime) inspectRoom(v *validated, ref string) error {
	zid, rid, ok := strings.Cut(ref, "/")
	if !ok || zid == "" || rid == "" {
		return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: fmt.Sprintf("%q is not <zone>/<room>", ref)}
	}
	z := v.zone(zid)
	room := findRoom(z, rid)
	if room == nil {
		return notFound("Room", ref)
	}

	type exitRow struct {
		Direction string `json:"direction"`
		ToZone    string `json:"to_zone"`
		ToRoom    string `json:"to_room"`
		// Reverse is the Direction back, when the target Room has an Exit
		// that way leading here; empty when the Exit is one-way.
		Reverse string `json:"reverse"`
	}
	byDir := map[string]*contentv1.ExitDefinition{}
	for _, e := range room.GetExits() {
		byDir[e.GetDirection()] = e
	}
	var rows []exitRow
	// The closed Direction order, compass first (sim.Directions), rather
	// than the compiled order, which sorts them as strings.
	for _, d := range sim.Directions() {
		e, ok := byDir[string(d)]
		if !ok {
			continue
		}
		toZone := e.GetToZone()
		if toZone == "" {
			toZone = zid
		}
		row := exitRow{Direction: string(d), ToZone: toZone, ToRoom: e.GetToRoom()}
		if rev, ok := d.Reverse(); ok {
			if back := findRoom(v.zone(toZone), e.GetToRoom()); back != nil && leadsTo(back, toZone, string(rev), zid, rid) {
				row.Reverse = string(rev)
			}
		}
		rows = append(rows, row)
	}
	type compRow struct {
		Type   string            `json:"type"`
		Fields map[string]string `json:"fields,omitempty"`
	}
	var comps []compRow
	for _, c := range room.GetComponents() {
		cr := compRow{Type: c.GetType()}
		for _, f := range c.GetFields() {
			if cr.Fields == nil {
				cr.Fields = map[string]string{}
			}
			cr.Fields[f.GetName()] = fieldValue(f)
		}
		comps = append(comps, cr)
	}

	if rt.settings.Output == outputJSON {
		return rt.writeJSON(struct {
			Zone        string    `json:"zone"`
			ID          string    `json:"id"`
			Title       string    `json:"title"`
			Description string    `json:"description"`
			Components  []compRow `json:"components"`
			Exits       []exitRow `json:"exits"`
		}{zid, rid, room.GetTitle(), room.GetDescription(), comps, rows})
	}
	tw := tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "room:\t%s/%s\n", zid, rid)
	fmt.Fprintf(tw, "title:\t%s\n", lang.Quote(room.GetTitle()))
	fmt.Fprintf(tw, "desc:\t%s\n", orNone(lang.Quote(room.GetDescription())))
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(rt.stdout, "components:")
	if len(comps) == 0 {
		fmt.Fprintln(rt.stdout, "  (none)")
	}
	for _, c := range room.GetComponents() {
		fmt.Fprintf(rt.stdout, "  %s\n", renderComponent(c))
	}
	fmt.Fprintln(rt.stdout, "exits:")
	if len(rows) == 0 {
		fmt.Fprintln(rt.stdout, "  (none)")
	}
	tw = tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		target := r.ToRoom
		if r.ToZone != zid {
			target = r.ToZone + "." + r.ToRoom // the source notation for another Zone
		}
		mark := "one-way: no Exit back"
		if r.Reverse != "" {
			mark = "back: " + r.Reverse
		}
		fmt.Fprintf(tw, "  %s\t-> %s\t%s\n", r.Direction, target, mark)
	}
	return tw.Flush()
}

func findRoom(z *contentv1.ZoneDefinition, id string) *contentv1.RoomDefinition {
	for _, r := range z.GetRooms() {
		if r.GetId() == id {
			return r
		}
	}
	return nil
}

// leadsTo reports whether room, in Zone zid, has an Exit dir to toZone/toRoom.
func leadsTo(room *contentv1.RoomDefinition, zid, dir, toZone, toRoom string) bool {
	for _, e := range room.GetExits() {
		tz := e.GetToZone()
		if tz == "" {
			tz = zid
		}
		if e.GetDirection() == dir && tz == toZone && e.GetToRoom() == toRoom {
			return true
		}
	}
	return false
}

// --- template ----------------------------------------------------------------

// allTemplates is every Template the pack resolves against: its own, and
// the core's.
func (v *validated) allTemplates() map[string]*contentv1.TemplateDefinition {
	out := map[string]*contentv1.TemplateDefinition{}
	if v.corePack != nil {
		for _, t := range v.corePack.Templates {
			out[t.GetName()] = t
		}
	}
	for _, t := range v.templates() {
		out[t.GetName()] = t
	}
	return out
}

func (rt *runtime) inspectTemplate(v *validated, ref string) error {
	all := v.allTemplates()
	t, ok := all[ref]
	if !ok {
		return notFound("Template", ref)
	}
	from := map[string]string{}
	for _, p := range t.GetProvenance() {
		from[p.GetComponent()+"\x00"+p.GetField()] = p.GetFrom()
	}

	type fieldRow struct {
		Component string `json:"component"`
		Field     string `json:"field"`
		Value     string `json:"value"`
		From      string `json:"from"`
	}
	type compRow struct {
		Type string `json:"type"`
		// From is the first Template in the chain that carries the
		// Component: the one that added it.
		From string `json:"from"`
	}
	var fields []fieldRow
	var comps []compRow
	for _, c := range t.GetComponents() {
		comps = append(comps, compRow{Type: c.GetType(), From: introducedBy(all, t.GetChain(), c.GetType())})
		for _, f := range c.GetFields() {
			fields = append(fields, fieldRow{Component: c.GetType(), Field: f.GetName(), Value: fieldValue(f), From: from[c.GetType()+"\x00"+f.GetName()]})
		}
	}
	src := ""
	if s := t.GetSource(); s != nil && s.GetFile() != "" {
		src = fmt.Sprintf("%s:%d", s.GetFile(), s.GetLine())
	}
	kind := t.GetKind().String()

	if rt.settings.Output == outputJSON {
		return rt.writeJSON(struct {
			Name       string     `json:"name"`
			Kind       string     `json:"kind"`
			Chain      []string   `json:"chain"`
			Source     string     `json:"source"`
			Components []compRow  `json:"components"`
			Fields     []fieldRow `json:"fields"`
		}{t.GetName(), kind, t.GetChain(), src, comps, fields})
	}
	tw := tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "template:\t%s\n", t.GetName())
	fmt.Fprintf(tw, "kind:\t%s\n", kind)
	fmt.Fprintf(tw, "chain:\t%s\n", strings.Join(t.GetChain(), " > "))
	fmt.Fprintf(tw, "source:\t%s\n", orNone(src))
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(rt.stdout, "components:")
	if len(comps) == 0 {
		fmt.Fprintln(rt.stdout, "  (none)")
	}
	// One line per field, as `Type.field = value  (from)`; a marker Component,
	// which sets no field, is one line naming the Template that added it.
	for _, c := range t.GetComponents() {
		short := shortType(c.GetType())
		if len(c.GetFields()) == 0 {
			fmt.Fprintf(rt.stdout, "  %s  (%s)\n", short, introducedBy(all, t.GetChain(), c.GetType()))
			continue
		}
		for _, f := range c.GetFields() {
			fmt.Fprintf(rt.stdout, "  %s.%s = %s  (%s)\n", short, f.GetName(), fieldValue(f), from[c.GetType()+"\x00"+f.GetName()])
		}
	}
	return nil
}

// introducedBy is the first Template in chain, root first, whose flattened
// Components include typ. Every Template's Component set holds its
// ancestors', so that is the one that added it.
func introducedBy(all map[string]*contentv1.TemplateDefinition, chain []string, typ string) string {
	for _, ref := range chain {
		for _, c := range all[ref].GetComponents() {
			if c.GetType() == typ {
				return ref
			}
		}
	}
	return ""
}

// --- rendering ---------------------------------------------------------------

// shortType is a Component type without its namespace: andara.core.Behavior
// reads as Behavior, which is how AC-7 prints it.
func shortType(t string) string {
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t[i+1:]
	}
	return t
}

func componentTypes(cs []*contentv1.ComponentValue) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.GetType())
	}
	return out
}

func renderComponent(c *contentv1.ComponentValue) string {
	if len(c.GetFields()) == 0 {
		return c.GetType()
	}
	parts := make([]string, 0, len(c.GetFields()))
	for _, f := range c.GetFields() {
		parts = append(parts, f.GetName()+": "+fieldValue(f))
	}
	return c.GetType() + " { " + strings.Join(parts, ", ") + " }"
}

// fieldValue renders a Component field in the notation the Builder wrote it.
func fieldValue(f *contentv1.ComponentField) string {
	switch v := f.GetValue().(type) {
	case *contentv1.ComponentField_StringValue:
		return lang.Quote(v.StringValue)
	case *contentv1.ComponentField_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *contentv1.ComponentField_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	}
	return ""
}

func orNone(s string) string {
	if s == "" || s == `""` {
		return "(none)"
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
