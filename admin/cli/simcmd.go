// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/sim"
)

// sim repl drives the Command Pipeline in-process against content on disk
// with a fake log: parse and authorize as the Gateway would, append to an
// in-memory Partition, tick the engine, print the Events. It is how the
// pipeline is exercised before AW-SRV-010 puts a broker behind Submit, and
// it is not a connection to a server: nothing here talks to one, so the
// global --server-address and credentials are read and unused.
//
// The one thing it does that a Gateway may not is read World state to keep
// the Session's Zone binding current after a cross-Zone move. A Gateway
// learns that from the Event stream (AW-SRV-010); a harness that owns the
// engine can just look.
func newSimCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sim",
		Short:         "Run the simulation in-process against content on disk (developer)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newSimReplCmd(rt))
	return cmd
}

func newSimReplCmd(rt *runtime) *cobra.Command {
	var (
		contentDir string
		character  string
		start      string
		seed       uint64
		maxIntent  int
		verbTable  string
		taps       []string
	)
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Read Intents from stdin, run them through parse → authorize → [log] → validate → apply, print the Events",
		Long: `Read Intents from stdin, one per line, and drive them through the Command
Pipeline in-process: parse and authorize as the Gateway would, append to an
in-memory log, tick the engine, and print what the tick emitted. A pre-log
rejection is reported as such and reaches no log; a post-log rejection is a
CommandRejected Event with the offset it consumed.

The Character named by --character is placed in --start (zone/room; default
the first Room of the first Zone) before the first Intent. What is printed is
what that Character's Session perceives — the sim's Scope applied by the same
fan-out a Session stream reads from. --tap-events adds observers elsewhere,
so scoping can be watched from two Rooms at once.`,
		Example: `  andara-cli sim repl --content ./testdata/content/valid
  > look
  > north
  > west          # no_such_exit, post-log
  > frobnicate    # unknown_verb, pre-log, nothing in the log

  andara-cli sim repl --content ./testdata/content/valid --start town/plaza --tap-events town/hall,docks/pier
  > north         # the tap in the hall sees the arrival; the pier sees nothing`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if contentDir == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--content is required", Detail: map[string]any{"flag": "--content"}}
			}
			r, err := newRepl(rt, replOptions{content: contentDir, character: character, start: start, seed: seed, maxIntent: maxIntent, verbTable: verbTable, taps: taps})
			if err != nil {
				return err
			}
			return r.run(rt.stdinReader())
		},
	}
	cmd.Flags().StringVar(&contentDir, "content", "", "content directory: zone files, with templates/ beneath it")
	cmd.Flags().StringVar(&character, "character", "you", "the Character to bind the Session to")
	cmd.Flags().StringVar(&start, "start", "", "zone/room to place the Character in (default: the first Room of the first Zone)")
	cmd.Flags().Uint64Var(&seed, "seed", 0, "PRNG seed; 0 derives one from the World")
	cmd.Flags().IntVar(&maxIntent, "max-intent-bytes", command.DefaultMaxIntentBytes, "largest Intent parse will read")
	cmd.Flags().StringVar(&verbTable, "verb-table", "", "JSON verb table replacing the built-in one")
	cmd.Flags().StringSliceVar(&taps, "tap-events", nil, "also print the scoped Event stream of an observer in zone/room (repeatable); \"world\" taps a Game Master's world view")
	return cmd
}

func (rt *runtime) stdinReader() io.Reader {
	if rt.stdin != nil {
		return rt.stdin
	}
	return os.Stdin
}

type replOptions struct {
	content   string
	character string
	start     string
	seed      uint64
	maxIntent int
	verbTable string
	taps      []string
}

// repl is the harness: the pre-log pipeline, the fake log, and the engine.
type repl struct {
	rt       *runtime
	engine   *sim.Engine
	pipeline *command.Pipeline
	log      map[int32][]sim.Record
	actor    sim.EntityID
	zone     sim.ZoneID
	hub      *events.Hub
	me       *events.Subscription
	taps     []tap
}

// tap is one extra observer whose stream is printed with a label.
type tap struct {
	label string
	sub   *events.Subscription
}

const replSession = "repl"

func newRepl(rt *runtime, o replOptions) (*repl, error) {
	inputs, errs := content.LoadDir(o.content)
	tinputs, terrs := content.LoadTemplatesDir(o.content)
	errs = append(errs, terrs...)
	var world *sim.World
	if len(inputs) > 0 {
		var berrs []sim.ValidationError
		world, berrs = sim.BuildWorld(inputs, sim.Options{})
		errs = append(errs, berrs...)
	}
	var fatal []string
	for _, e := range errs {
		if !sim.IsWarning(e, false) {
			fatal = append(fatal, e.Error())
		}
	}
	if world == nil && len(fatal) == 0 {
		fatal = append(fatal, "no zones found in "+o.content)
	}
	if len(fatal) > 0 {
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "content refused: " + fatal[0], Detail: map[string]any{"findings": fatal}}
	}
	templates, terrs2 := sim.BuildTemplates(tinputs, sim.TemplateOptions{})
	for _, e := range terrs2 {
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "templates refused: " + e.Error()}
	}

	table := command.Builtin()
	if o.verbTable != "" {
		t, err := command.LoadVerbTable(o.verbTable)
		if err != nil {
			return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: err.Error()}
		}
		table = t
	}

	zone, room, err := startRoom(world, o.start)
	if err != nil {
		return nil, err
	}
	r := &repl{rt: rt, log: map[int32][]sim.Record{}, actor: sim.EntityID(o.character), zone: zone}
	r.engine = sim.NewEngine(world, templates, sim.Config{Seed: o.seed, Partitions: allPartitions(), Handlers: sim.Handlers()})
	r.engine.State().Zones[zone].Entities[r.actor] = &sim.EntityState{ID: r.actor, Template: "andara.core.Character", Room: room}
	// The same fan-out a Session stream reads from, so what the repl
	// prints is exactly what a Session would be sent.
	r.hub = events.New(events.Options{Buffer: 256, MaxSubscribers: 64})
	r.engine.Subscribe(r.hub)
	player := auth.Principal{AccountID: "repl", Roles: []auth.Role{auth.RolePlayer}}
	me, err := r.hub.Subscribe(context.Background(), events.Subscriber{
		Observer: events.Observer{Entity: r.actor, Room: sim.RoomRef{Zone: zone, Room: room}}, Principal: player, SessionID: replSession,
	})
	if err != nil {
		return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
	}
	r.me = me
	for _, t := range o.taps {
		obs := events.Observer{}
		principal := player
		if t == "world" {
			obs.World = true
			principal = auth.Principal{AccountID: "repl-gm", Roles: []auth.Role{auth.RoleGameMaster}}
		} else {
			zid, rid, ok := strings.Cut(t, "/")
			if !ok {
				return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--tap-events takes zone/room or world", Detail: map[string]any{"flag": "--tap-events", "value": t}}
			}
			if _, ok := world.Resolve(sim.RoomRef{Zone: sim.ZoneID(zid), Room: sim.RoomID(rid)}); !ok {
				return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "no such room " + t, Detail: map[string]any{"flag": "--tap-events", "value": t}}
			}
			obs.Room = sim.RoomRef{Zone: sim.ZoneID(zid), Room: sim.RoomID(rid)}
		}
		sub, err := r.hub.Subscribe(context.Background(), events.Subscriber{Observer: obs, Principal: principal, SessionID: "tap-" + t})
		if err != nil {
			return nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: err.Error()}
		}
		r.taps = append(r.taps, tap{label: t, sub: sub})
	}
	r.pipeline = &command.Pipeline{
		Table:    table,
		MaxBytes: o.maxIntent,
		Bindings: command.BinderFunc(func(id string) (command.Binding, bool) {
			if id != replSession || r.zone == "" {
				return command.Binding{}, false
			}
			return command.Binding{Actor: r.actor, Zone: r.zone}, true
		}),
		Log:    command.ProducerFunc(r.produce),
		Tracer: rt.tp.Tracer("andara-cli"),
	}
	return r, nil
}

// startRoom resolves --start, or picks the first Room of the first Zone.
func startRoom(w *sim.World, start string) (sim.ZoneID, sim.RoomID, error) {
	if start != "" {
		zid, rid, ok := strings.Cut(start, "/")
		if !ok {
			return "", "", &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--start must be zone/room", Detail: map[string]any{"flag": "--start", "value": start}}
		}
		if _, ok := w.Resolve(sim.RoomRef{Zone: sim.ZoneID(zid), Room: sim.RoomID(rid)}); !ok {
			return "", "", &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "no such room " + start, Detail: map[string]any{"flag": "--start", "value": start}}
		}
		return sim.ZoneID(zid), sim.RoomID(rid), nil
	}
	zids := make([]string, 0, len(w.Zones))
	for id := range w.Zones {
		zids = append(zids, string(id))
	}
	sort.Strings(zids)
	z := w.Zones[sim.ZoneID(zids[0])]
	rids := make([]string, 0, len(z.Rooms))
	for id := range z.Rooms {
		rids = append(rids, string(id))
	}
	sort.Strings(rids)
	return z.ID, sim.RoomID(rids[0]), nil
}

func allPartitions() []int32 {
	out := make([]int32, sim.PartitionCount)
	for i := range out {
		out[i] = int32(i)
	}
	return out
}

// produce is the fake log: append to the Zone's Partition.
func (r *repl) produce(_ context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	rec := sim.Record{Partition: p, Offset: r.engine.State().Offsets[p] + int64(len(r.log[p])), Command: cmd}
	r.log[p] = append(r.log[p], rec)
	return command.Accepted{Partition: p, Offset: rec.Offset}, nil
}

// run reads Intents until EOF.
func (r *repl) run(in io.Reader) error {
	principal := auth.Principal{AccountID: "repl", Roles: []auth.Role{auth.RolePlayer}}
	r.prompt()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		raw := sc.Text()
		n++
		if strings.TrimSpace(raw) == "" {
			r.prompt()
			continue
		}
		acc, err := r.pipeline.Submit(context.Background(), command.Intent{SessionID: replSession, Raw: raw, ClientRef: fmt.Sprint(n)}, principal)
		if err != nil {
			if e, ok := command.AsError(err); ok {
				r.emit(fmt.Sprintf("rejected (pre-log, %s): %s: %s — nothing in the log", e.Stage, e.Code, e.Detail),
					map[string]any{"rejected": map[string]any{"stage": string(e.Stage), "code": e.Code, "detail": e.Detail, "pre_log": true}})
			} else {
				r.emit("error: "+err.Error(), map[string]any{"error": err.Error()})
			}
			r.prompt()
			continue
		}
		r.emit(fmt.Sprintf("accepted: %s at partition %d offset %d", acc.Verb, acc.Partition, acc.Offset),
			map[string]any{"accepted": map[string]any{"verb": acc.Verb, "partition": acc.Partition, "offset": acc.Offset}})
		if err := r.tick(); err != nil {
			return err
		}
		r.prompt()
	}
	if err := sc.Err(); err != nil {
		return &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "reading stdin: " + err.Error()}
	}
	if r.rt.settings.Output != outputJSON {
		_, _ = fmt.Fprintln(r.rt.stdout)
	}
	return nil
}

// tick steps the engine over everything in the fake log, feeding cross-Zone
// Commands back in and stepping again until nothing is pending — one tick
// per hop, as the broker would order it — then re-reads where the actor is.
func (r *repl) tick() error {
	for hops := 0; hops < 8 && len(r.log) > 0; hops++ {
		var in sim.TickInput
		parts := make([]int, 0, len(r.log))
		for p := range r.log {
			parts = append(parts, int(p))
		}
		sort.Ints(parts)
		for _, p := range parts {
			in.Records = append(in.Records, r.log[int32(p)]...)
		}
		r.log = map[int32][]sim.Record{}
		res, err := r.engine.Step(in)
		if err != nil {
			return &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "tick: " + err.Error()}
		}
		// Deliver, then print what each observer was sent — in step with
		// the tick, so the transcript is deterministic.
		r.hub.Flush()
		for _, d := range pending(r.me) {
			r.printDelivery("", d)
		}
		for _, t := range r.taps {
			for _, d := range pending(t.sub) {
				r.printDelivery(t.label, d)
			}
		}
		for _, out := range res.Outbound {
			if _, err := r.produce(context.Background(), out); err != nil {
				return err
			}
		}
	}
	// Where is the actor now? A Gateway learns this from CharacterArrived;
	// the harness owns the engine and looks.
	r.zone = ""
	for zid, z := range r.engine.State().Zones {
		if _, ok := z.Entities[r.actor]; ok {
			r.zone = zid
		}
	}
	return nil
}

// pending reads what a subscription has been sent so far.
func pending(s *events.Subscription) []events.Delivery {
	var out []events.Delivery
	for {
		select {
		case d, ok := <-s.Events():
			if !ok {
				return out
			}
			out = append(out, d)
		default:
			return out
		}
	}
}

func (r *repl) printDelivery(label string, d events.Delivery) {
	env := d.Envelope
	j := map[string]any{"event_id": d.ID, "tick": uint64(d.Tick), "type": string(d.Type), "client_ref": env.GetClientRef()}
	if label != "" {
		j["observer"] = label
	}
	var human string
	switch p := env.GetPayload().(type) {
	case *gamev1.EventEnvelope_RoomDescribed:
		rd := p.RoomDescribed
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n  %s\n", rd.GetTitle(), rd.GetDescription())
		if len(rd.GetExits()) > 0 {
			fmt.Fprintf(&b, "  exits: %s\n", strings.Join(rd.GetExits(), ", "))
		} else {
			b.WriteString("  exits: none\n")
		}
		if len(rd.GetOccupants()) > 0 {
			fmt.Fprintf(&b, "  here: %s\n", strings.Join(rd.GetOccupants(), ", "))
		}
		human = strings.TrimRight(b.String(), "\n")
		j["room"] = map[string]any{"zone": rd.GetZoneId(), "room": rd.GetRoomId(), "title": rd.GetTitle(), "description": rd.GetDescription(), "exits": rd.GetExits(), "occupants": rd.GetOccupants()}
	case *gamev1.EventEnvelope_CharacterLeft:
		human = fmt.Sprintf("%s leaves %s.", p.CharacterLeft.GetCharacterName(), p.CharacterLeft.GetToDirection())
		j["character"], j["room"], j["direction"] = p.CharacterLeft.GetCharacterName(), p.CharacterLeft.GetRoomId(), p.CharacterLeft.GetToDirection()
	case *gamev1.EventEnvelope_CharacterArrived:
		from := p.CharacterArrived.GetFromDirection()
		if from == "" {
			human = fmt.Sprintf("%s arrives.", p.CharacterArrived.GetCharacterName())
		} else {
			human = fmt.Sprintf("%s arrives from the %s.", p.CharacterArrived.GetCharacterName(), from)
		}
		j["character"], j["room"], j["direction"] = p.CharacterArrived.GetCharacterName(), p.CharacterArrived.GetRoomId(), from
	case *gamev1.EventEnvelope_CommandRejected:
		human = fmt.Sprintf("rejected (post-log): %s: %s", p.CommandRejected.GetCode(), p.CommandRejected.GetMessage())
		j["rejected"] = map[string]any{"code": p.CommandRejected.GetCode(), "message": p.CommandRejected.GetMessage(), "pre_log": false}
	case *gamev1.EventEnvelope_ZoneFaulted:
		human = "zone faulted"
		if z := p.ZoneFaulted.GetZoneId(); z != "" {
			human += ": " + z
		}
	case *gamev1.EventEnvelope_SubscriberDropped:
		human = "dropped: " + p.SubscriberDropped.GetReason()
	default:
		human = string(d.Type)
	}
	prefix := fmt.Sprintf("[tick %d] ", d.Tick)
	if label != "" {
		prefix = fmt.Sprintf("[tick %d, %s] ", d.Tick, label)
	}
	r.emit(prefix+human, j)
}

func (r *repl) emit(human string, j map[string]any) {
	if r.rt.settings.Output == outputJSON {
		_ = r.rt.writeJSON(j)
		return
	}
	_, _ = fmt.Fprintln(r.rt.stdout, human)
}

func (r *repl) prompt() {
	if r.rt.settings.Output != outputJSON {
		_, _ = fmt.Fprint(r.rt.stdout, "> ")
	}
}
