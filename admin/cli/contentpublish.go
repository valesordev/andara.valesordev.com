// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/content"
)

// The Builder's publish path (AW-CLI-003): publish, approve, activate, roll
// back, and read the history, each one-to-one onto AW-SRV-013's Admin RPCs.
// The server is the security boundary. These commands enforce nothing; they
// show approval state before a live-world change and render the server's
// refusal in terms a Builder can act on.

// contentDomain is ErrorInfo.domain on every publish-path refusal.
const contentDomain = "andara.content"

// CodeYesRequired is a live-world change asked for without --yes where no
// one is at a terminal to confirm it.
const CodeYesRequired = "yes_required"

// spanUnder opens a span under ctx, which carries the command's root span or
// a child of it. A no-op span when telemetry never started.
func (rt *runtime) spanUnder(ctx context.Context, name string) (context.Context, trace.Span) {
	if rt.tp == nil {
		return ctx, noop.Span{}
	}
	return rt.tp.Tracer("andara-cli").Start(ctx, name)
}

// commandAttrs puts the pack and version on the command's root span, so one
// trace joins the Builder's command to the server's handling of it.
func (rt *runtime) commandAttrs(pack string, version uint64) {
	if rt.span == nil {
		return
	}
	rt.span.SetAttributes(attribute.String("pack", pack))
	if version != 0 {
		rt.span.SetAttributes(attribute.Int64("version", int64(version)))
	}
}

// --- refusals ----------------------------------------------------------------

// refusal reads a publish-path refusal off a wire error: the ErrorInfo
// reason, the server's message, and the status details.
type refusal struct {
	reason   string
	message  string
	code     connect.Code
	findings []*contentv1.Diagnostic
	act      *adminv1.ActivationRefusal
}

func readRefusal(err error) (refusal, bool) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return refusal{}, false
	}
	r := refusal{message: ce.Message(), code: ce.Code()}
	r.reason, _ = errorInfo(err)
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		switch m := msg.(type) {
		case *adminv1.PublishFindings:
			r.findings = m.GetFindings()
		case *adminv1.ActivationRefusal:
			r.act = m
		}
	}
	return r, r.reason != ""
}

// contentError is a refused publish-path call as the CLI reports it: exit 1
// with the server's reason as error.code (AW-SRV-013's taxonomy). Anything
// without a reason (unreachable, timeout, unauthenticated) is rpcError's.
func (rt *runtime) contentError(err error) error {
	r, ok := readRefusal(err)
	if !ok {
		return rt.withTrace(rpcError(err))
	}
	return rt.withTrace(&AppError{Exit: ExitFail, Code: r.reason, Message: r.message,
		Detail: map[string]any{"grpc_code": r.code.String(), "domain": contentDomain}})
}

// withTrace adds the command's trace_id to an error's detail, so an operator
// can go from a Builder's report to the trace (AW-CLI-003 Observability).
func (rt *runtime) withTrace(err error) error {
	var ae *AppError
	if errors.As(err, &ae) {
		if ae.Detail == nil {
			ae.Detail = map[string]any{}
		}
		if id := rt.traceID(); id != "" {
			ae.Detail["trace_id"] = id
		}
	}
	return err
}

// --- confirmation ------------------------------------------------------------

func (rt *runtime) interactive() bool {
	if rt.tty != nil {
		return rt.tty()
	}
	return rt.stdin == nil && isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func (rt *runtime) input() io.Reader {
	if rt.stdin != nil {
		return rt.stdin
	}
	return os.Stdin
}

// confirm shows what a live-world change will do and waits for y, unless
// --yes. With no one at a terminal and no --yes it refuses rather than
// hanging (AC-9). The text is logged at info with the trace_id the RPCs
// carry, so the CLI's line joins the server's audit record.
func (rt *runtime) confirm(text, question string, yes bool) error {
	rt.log("info", "confirmation: "+strings.ReplaceAll(text, "\n", "; ")+" (trace_id "+rt.traceID()+")")
	if rt.span != nil {
		rt.span.SetAttributes(attribute.String("confirmation", text))
	}
	if yes {
		return nil
	}
	if !rt.interactive() {
		return &AppError{Exit: ExitUsage, Code: CodeYesRequired,
			Message: "--yes required: this changes the live world, and there's no terminal to confirm it at",
			Detail:  map[string]any{"flag": "--yes"}}
	}
	if text != "" {
		fmt.Fprintln(rt.stderr, text)
	}
	fmt.Fprintf(rt.stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(rt.input()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return &AppError{Exit: ExitFail, Code: "declined", Message: "not confirmed; nothing was changed"}
	}
	return nil
}

// --- shared reads ------------------------------------------------------------

// who names an Account in output: the caller's username for the caller,
// the account ID otherwise. No RPC reads another Account's username.
func (rt *runtime) who(accountID string) string {
	if accountID == "" {
		return "nobody"
	}
	if cred, err := rt.loadCredential(); err == nil && cred != nil && cred.Username != "" && accountID != "" && accountID == callerID(cred) {
		return cred.Username
	}
	return accountID
}

// callerID is the Account the stored credential speaks for, read from its
// token unverified: it names a caller in a prompt, and the server decides
// everything else.
func callerID(cred *storedCredential) string {
	if id, ok := auth.TokenCaller(cred.SessionToken); ok {
		return id
	}
	return cred.AccountID
}

// asFlag registers --as on a content write command and returns where its
// value lands. The command's RunE hands it to useActAs before its first RPC.
func asFlag(cmd *cobra.Command) *string {
	var as string
	cmd.Flags().StringVar(&as, "as", "", "run as this account ID (operator or game master; every call is audited)")
	return &as
}

// useActAs makes the command's Admin calls act as the --as account.
func (rt *runtime) useActAs(as string) { rt.actAs = strings.TrimSpace(as) }

func stamp(unixNano int64) string {
	if unixNano == 0 {
		return ""
	}
	return time.Unix(0, unixNano).UTC().Format(time.RFC3339)
}

func parseVersion(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: fmt.Sprintf("%q is not a version; versions are 1, 2, …", s)}
	}
	return v, nil
}

// --- publish -----------------------------------------------------------------

func newContentPublishCmd(rt *runtime) *cobra.Command {
	var path, pack, cache string
	var as *string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Compile, validate, and publish a pack as a new version, awaiting approval",
		Long: "Compile and validate the pack under --path as `content validate` does, upload the\n" +
			"blobs the server doesn't already hold, and publish them as the pack's next version.\n\n" +
			"The new version's parent is the newest version the server has. Publishing never\n" +
			"moves the Active Pointer: a version needs a second approver, then `content activate`.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			rt.useActAs(*as)
			return rt.publish(path, pack, cache)
		},
	}
	as = asFlag(cmd)
	fs := cmd.Flags()
	fs.StringVar(&path, "path", ".", "pack directory of .aw source")
	fs.StringVar(&pack, "pack", "", "the pack to publish as (default: the source's `pack` declaration)")
	fs.StringVar(&cache, "cache", "", "core pack cache (default $ANDARA_CONTENT_CACHE, then ~/.cache/andara/packs)")
	return cmd
}

func (rt *runtime) publish(dir, pack, cacheFlag string) error {
	v, err := rt.validatePath(dir, pack, cacheFlag)
	if err != nil {
		return err
	}
	if v.failed() {
		// Local findings are the fast loop; nothing reaches the server.
		return rt.reportValidated(v)
	}
	out := v.compiled
	pack = out.Pack
	rt.commandAttrs(pack, 0)

	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	pctx, span := rt.spanUnder(ctx, "content.publish")
	defer span.End()

	refs := make([]*contentv1.BlobRef, 0, len(out.Blobs))
	var total int64
	for _, b := range out.Blobs {
		sum := sha256.Sum256(b.Bytes)
		refs = append(refs, &contentv1.BlobRef{Path: b.Path, Hash: sum[:], SizeBytes: uint64(len(b.Bytes))})
		total += int64(len(b.Bytes))
	}

	uploaded, uploadedBytes := 0, int64(0)
	var parent uint64
	if pack != core.Pack {
		// andara.core goes straight to PublishVersion, which refuses it for
		// everyone with the server's own message (AC-14). HasBlobs would
		// refuse a Builder first, as pack_not_held, and say less.
		list, err := client.ListVersions(pctx, connect.NewRequest(&adminv1.ListVersionsRequest{PackId: pack, Limit: 1}))
		if err != nil {
			return rt.contentError(err)
		}
		if vs := list.Msg.GetVersions(); len(vs) > 0 {
			parent = vs[0].GetVersion()
		}
		missing, err := rt.missingBlobs(pctx, client, pack, refs)
		if err != nil {
			return err
		}
		for _, i := range missing {
			if err := rt.uploadBlob(pctx, client, pack, out.Blobs[i], refs[i]); err != nil {
				return err
			}
			uploaded++
			uploadedBytes += int64(len(out.Blobs[i].Bytes))
		}
	}
	span.SetAttributes(
		attribute.Int("blobs_total", len(refs)),
		attribute.Int("blobs_uploaded", uploaded),
		attribute.Int64("bytes", uploadedBytes),
	)

	if rt.beforePublishVersion != nil {
		rt.beforePublishVersion()
	}
	resp, err := client.PublishVersion(pctx, connect.NewRequest(&adminv1.PublishVersionRequest{PackId: pack, Blobs: refs, ParentVersion: parent}))
	if err != nil {
		return rt.publishRefused(err, v, pack, parent)
	}
	version := resp.Msg.GetVersion()
	rt.commandAttrs(pack, version)

	// Local warnings and the server's, placed on the source and listed once
	// (the server's gate raises the same warnings, and may add some a
	// laptop can't: against the other packs in effect).
	diags := mergeDiagnostics(v.diags, placeAll(out.SourceMap, resp.Msg.GetWarnings()))
	parentText := fmt.Sprintf("parent %d", parent)
	if parent == 0 {
		parentText = "first version"
	}
	line := fmt.Sprintf("%s@%d published (%s), awaiting approval", pack, version, parentText)
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{
			"pack": pack, "version": version, "parent_version": parent, "core_version": resp.Msg.GetCoreVersion(),
			"blobs_total": len(refs), "blobs_uploaded": uploaded, "bytes_uploaded": uploadedBytes, "bytes_total": total,
			"diagnostics": toJSONDiagnostics(diags), "trace_id": rt.traceID(),
		})
	}
	if err := rt.writeDiagnostics(diags, dir); err != nil {
		return err
	}
	_, err = fmt.Fprintln(rt.stdout, line)
	return err
}

// missingBlobs asks the server which blobs it doesn't hold, at most
// content.MaxHasBlobs hashes per call, and returns their indexes.
func (rt *runtime) missingBlobs(ctx context.Context, client adminv1connect.AdminClient, pack string, refs []*contentv1.BlobRef) ([]int, error) {
	var missing []int
	for start := 0; start < len(refs); start += content.MaxHasBlobs {
		end := min(start+content.MaxHasBlobs, len(refs))
		hashes := make([][]byte, 0, end-start)
		for _, r := range refs[start:end] {
			hashes = append(hashes, r.GetHash())
		}
		resp, err := client.HasBlobs(ctx, connect.NewRequest(&adminv1.HasBlobsRequest{PackId: pack, Hashes: hashes}))
		if err != nil {
			return nil, rt.contentError(err)
		}
		for i, present := range resp.Msg.GetPresent() {
			if !present {
				missing = append(missing, start+i)
			}
		}
	}
	return missing, nil
}

// uploadBlob is one PublishBlob stream, and one span: a header, then the body
// in chunks of at most content.BlobChunkBytes.
func (rt *runtime) uploadBlob(ctx context.Context, client adminv1connect.AdminClient, pack string, b lang.Blob, ref *contentv1.BlobRef) error {
	ctx, span := rt.spanUnder(ctx, "content.publish_blob")
	defer span.End()
	span.SetAttributes(attribute.String("path", b.Path), attribute.Int("bytes", len(b.Bytes)))
	stream := client.PublishBlob(ctx)
	if err := stream.Send(&adminv1.PublishBlobRequest{Chunk: &adminv1.PublishBlobRequest_Header{Header: &adminv1.PublishBlobHeader{
		PackId: pack, Path: b.Path, MediaType: b.MediaType, SizeBytes: ref.GetSizeBytes(), Hash: ref.GetHash(),
	}}}); err != nil && !errors.Is(err, io.EOF) {
		return rt.contentError(err)
	}
	for off := 0; off < len(b.Bytes); off += content.BlobChunkBytes {
		end := min(off+content.BlobChunkBytes, len(b.Bytes))
		if err := stream.Send(&adminv1.PublishBlobRequest{Chunk: &adminv1.PublishBlobRequest_Data{Data: b.Bytes[off:end]}}); err != nil {
			if errors.Is(err, io.EOF) {
				break // the server answered early; CloseAndReceive has why
			}
			return rt.contentError(err)
		}
	}
	if _, err := stream.CloseAndReceive(); err != nil {
		return rt.contentError(err)
	}
	return nil
}

// publishRefused renders a refused PublishVersion. Findings print in the same
// format as local ones, placed on the source (AC-5). A stale parent is
// explained, not retried.
func (rt *runtime) publishRefused(err error, v *validated, pack string, parent uint64) error {
	r, ok := readRefusal(err)
	if !ok {
		return rt.withTrace(rpcError(err))
	}
	switch {
	case len(r.findings) > 0:
		v.diags = mergeDiagnostics(nil, placeAll(v.compiled.SourceMap, r.findings))
		if rt.settings.Output == outputJSON {
			if err := rt.writeJSON(toJSONDiagnostics(v.diags)); err != nil {
				return err
			}
			return rt.withTrace(&AppError{Exit: ExitFail, Code: r.reason, Rendered: true,
				Message: fmt.Sprintf("%s: the server refused the publish: %d finding(s)", pack, len(v.diags))})
		}
		if err := rt.writeDiagnostics(v.diags, v.label); err != nil {
			return err
		}
		return rt.withTrace(&AppError{Exit: ExitFail, Code: r.reason,
			Message: fmt.Sprintf("%s: the server refused the publish: %d finding(s)", pack, len(v.diags))})
	case r.reason == content.ErrReasonStaleParent:
		return rt.withTrace(&AppError{Exit: ExitFail, Code: r.reason,
			Message: fmt.Sprintf("%s: someone published after version %d while this was uploading (%s); run `content history %s` and publish again",
				pack, parent, r.message, pack)})
	}
	return rt.contentError(err)
}

// placeAll is the server's findings on the source that produced them, as
// `content validate` places the validator's.
func placeAll(smap *lang.SourceMap, fs []*contentv1.Diagnostic) []lang.Diagnostic {
	out := make([]lang.Diagnostic, 0, len(fs))
	for _, f := range fs {
		sev := lang.SeverityError
		if f.GetSeverity() == contentv1.Severity_WARNING {
			sev = lang.SeverityWarning
		}
		if f.GetPack() != "" {
			// Another pack's finding (errors.md §1 rule 10.6), the mark of
			// which is a non-empty pack and nothing else: it is not on this
			// source, so it isn't placed by chain.
			out = append(out, lang.Diagnostic{File: f.GetFile(), Code: f.GetCode(), Message: f.GetMessage(), Severity: sev, Pack: f.GetPack()})
			continue
		}
		if d, ok := smap.Place(f.GetCode(), f.GetMessage(), f.GetChain(), sev); ok {
			out = append(out, d)
			continue
		}
		out = append(out, lang.Diagnostic{File: f.GetFile(), Line: int(f.GetLine()), Col: int(f.GetCol()),
			Code: f.GetCode(), Message: f.GetMessage(), Chain: f.GetChain(), Severity: sev})
	}
	return out
}

// --- approve -----------------------------------------------------------------

func newContentApproveCmd(rt *runtime) *cobra.Command {
	var yes bool
	var as *string
	cmd := &cobra.Command{
		Use:   "approve <pack> <version>",
		Short: "Approve a published version, so it can be activated",
		Long: "Record your approval of pack@version. A version needs a second Builder holding the\n" +
			"pack, or an Operator, before it can be activated. An Operator approving their own\n" +
			"publish is asked to confirm first.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			version, err := parseVersion(args[1])
			if err != nil {
				return err
			}
			rt.useActAs(*as)
			return rt.approve(args[0], version, yes)
		},
	}
	as = asFlag(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask before approving your own publish")
	return cmd
}

func (rt *runtime) approve(pack string, version uint64, yes bool) error {
	rt.commandAttrs(pack, version)
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	got, err := client.GetVersion(ctx, connect.NewRequest(&adminv1.GetVersionRequest{PackId: pack, Version: version}))
	if err != nil {
		return rt.contentError(err)
	}
	cred, _ := rt.loadCredential()
	if cred != nil && rt.publishedByCaller(cred, got.Msg.GetVersion()) {
		// AC-11: asked before the RPC. Whether it's allowed is the
		// server's call; this is making sure it's meant.
		q := fmt.Sprintf("You published %s@%d. Approve it yourself as %s?", pack, version, cred.Username)
		if err := rt.confirm("", q, yes); err != nil {
			return err
		}
	}
	// A fresh --timeout for the write: the prompt may have taken longer
	// than the reads' deadline.
	actx, acancel := rt.callCtx()
	defer acancel()
	resp, err := client.ApproveVersion(actx, connect.NewRequest(&adminv1.ApproveVersionRequest{PackId: pack, Version: version}))
	if err != nil {
		return rt.contentError(err)
	}
	line := fmt.Sprintf("%s@%d approved by %s", pack, version, rt.who(resp.Msg.GetApprovedBy()))
	if resp.Msg.GetSelfApproval() {
		line += " (self-approval: you published it)"
	}
	return rt.writeResult(line, map[string]any{
		"pack": pack, "version": version, "approved_by": resp.Msg.GetApprovedBy(),
		"approved_at": stamp(resp.Msg.GetApprovedAtUnixNano()), "self_approval": resp.Msg.GetSelfApproval(),
		"trace_id": rt.traceID(),
	})
}

// publishedByCaller reports whether the caller published cv: the manifest's
// author or publisher is the caller's Account, or the Account --as names
// (AW-SRV-039). The server reports publisher = author for a manifest written
// before that field, so the pair is read as the server sends it. It decides a
// prompt; the server decides everything else.
func (rt *runtime) publishedByCaller(cred *storedCredential, cv *contentv1.ContentVersion) bool {
	for _, id := range []string{cv.GetAuthor(), cv.GetPublisher()} {
		if id != "" && (id == callerID(cred) || id == rt.actAs) {
			return true
		}
	}
	return false
}

// --- activate and rollback ---------------------------------------------------

func newContentActivateCmd(rt *runtime) *cobra.Command {
	var yes, override bool
	var reason string
	var as *string
	cmd := &cobra.Command{
		Use:   "activate <pack> <version>",
		Short: "Move a pack's Active Pointer to an approved version",
		Long: "Make pack@version the version the World runs. It shows the pack, the version in\n" +
			"effect and the one replacing it, who published it and who approved it, and\n" +
			"waits for y unless --yes.\n\n" +
			"--override activates an unapproved version. It's for an Operator only, needs\n" +
			"--reason, and is audited as an override.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if override && strings.TrimSpace(reason) == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--override needs --reason: say why this skips approval",
					Detail: map[string]any{"flag": "--reason"}}
			}
			version, err := parseVersion(args[1])
			if err != nil {
				return err
			}
			rt.useActAs(*as)
			return rt.activate(args[0], version, false, yes, override, reason)
		},
	}
	as = asFlag(cmd)
	fs := cmd.Flags()
	fs.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	fs.BoolVar(&override, "override", false, "activate without approval (operator; needs --reason)")
	fs.StringVar(&reason, "reason", "", "why --override skips approval; recorded in the audit log")
	return cmd
}

func newContentRollbackCmd(rt *runtime) *cobra.Command {
	var yes bool
	var to uint64
	var as *string
	cmd := &cobra.Command{
		Use:   "rollback <pack>",
		Short: "Move a pack's Active Pointer back to the version active before this one",
		Long: "Activate the version that was active before the current one, or --to N. A version\n" +
			"once approved needs no fresh approval to go back to. Like activate, it shows what\n" +
			"changes and waits for y unless --yes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			rt.useActAs(*as)
			return rt.rollback(args[0], to, yes)
		},
	}
	as = asFlag(cmd)
	cmd.Flags().Uint64Var(&to, "to", 0, "the version to go back to (default: the one active before this)")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func (rt *runtime) rollback(pack string, to uint64, yes bool) error {
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	list, err := client.ListVersions(ctx, connect.NewRequest(&adminv1.ListVersionsRequest{PackId: pack}))
	if err != nil {
		return rt.contentError(err)
	}
	if to == 0 {
		to = previousActive(list.Msg.GetActiveVersion(), list.Msg.GetActivations())
		if to == 0 {
			return rt.withTrace(&AppError{Exit: ExitFail, Code: "no_previous_version",
				Message: fmt.Sprintf("%s has no earlier active version to roll back to; name one with --to", pack)})
		}
	}
	return rt.activate(pack, to, true, yes, false, "")
}

// previousActive is the version active before the current one: the newest
// pointer move to a version other than active.
func previousActive(active uint64, moves []*contentv1.ActiveVersion) uint64 {
	for i := len(moves) - 1; i >= 0; i-- {
		if v := moves[i].GetVersion(); v != active && v != 0 {
			return v
		}
	}
	return 0
}

func (rt *runtime) activate(pack string, version uint64, isRollback, yes, override bool, reason string) error {
	rt.commandAttrs(pack, version)
	if override && rt.span != nil {
		rt.span.SetAttributes(attribute.Bool("override", true), attribute.String("reason", reason))
	}
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	target, err := client.GetVersion(ctx, connect.NewRequest(&adminv1.GetVersionRequest{PackId: pack, Version: version}))
	if err != nil {
		return rt.contentError(err)
	}
	list, err := client.ListVersions(ctx, connect.NewRequest(&adminv1.ListVersionsRequest{PackId: pack, Limit: 1}))
	if err != nil {
		return rt.contentError(err)
	}
	cv := target.Msg.GetVersion()
	current := list.Msg.GetActiveVersion()

	verb := "activate"
	if isRollback {
		verb = "roll back to"
	}
	currentText := "nothing active"
	if current != 0 {
		currentText = fmt.Sprintf("%s@%d", pack, current)
	}
	approval := "not approved"
	if cv.GetApprovedBy() != "" {
		approval = fmt.Sprintf("approved by %s at %s", rt.who(cv.GetApprovedBy()), stamp(cv.GetApprovedAtUnixNano()))
	}
	text := fmt.Sprintf("%s %s@%d, replacing %s\n  %s@%d published by %s at %s, %s",
		verb, pack, version, currentText, pack, version, rt.who(cv.GetAuthor()), stamp(cv.GetPublishedAtUnixNano()), approval)
	if override {
		text += "\n  override: " + reason
	}
	if err := rt.confirm(text, "Proceed?", yes); err != nil {
		return err
	}

	// A fresh --timeout for the write: someone reading the confirmation
	// may take longer than the reads' deadline.
	actx, acancel := rt.callCtx()
	defer acancel()
	resp, err := client.ActivateVersion(actx, connect.NewRequest(&adminv1.ActivateVersionRequest{PackId: pack, Version: version, Override: override, Reason: reason}))
	if err != nil {
		return rt.activationRefused(err, pack, version, cv)
	}
	prev := resp.Msg.GetPreviousVersion()
	var line string
	switch {
	case resp.Msg.GetRollback():
		line = fmt.Sprintf("%s@%d active (rolled back from %s@%d)", pack, version, pack, prev)
	case prev == 0:
		line = fmt.Sprintf("%s@%d active (nothing was active)", pack, version)
	default:
		line = fmt.Sprintf("%s@%d active (was %s@%d)", pack, version, pack, prev)
	}
	return rt.writeResult(line, map[string]any{
		"pack": pack, "version": version, "previous_version": prev, "rollback": resp.Msg.GetRollback(),
		"override": override, "trace_id": rt.traceID(),
	})
}

// activationRefused renders AC-3's and AC-12's refusals in a Builder's terms.
func (rt *runtime) activationRefused(err error, pack string, version uint64, cv *contentv1.ContentVersion) error {
	r, ok := readRefusal(err)
	if !ok {
		return rt.withTrace(rpcError(err))
	}
	switch {
	case r.reason == content.ErrReasonUnapproved:
		return rt.withTrace(&AppError{Exit: ExitFail, Code: r.reason,
			Message: fmt.Sprintf("%s@%d needs approval by a second builder holding the pack or an operator (published by %s)",
				pack, version, rt.who(cv.GetAuthor())),
			Detail: map[string]any{"published_by": cv.GetAuthor()}})
	case r.act != nil:
		subjects := r.act.GetSubjects()
		if subjects == nil {
			subjects = []string{}
		}
		return rt.withTrace(&AppError{Exit: ExitFail, Code: r.act.GetReason(),
			Message: fmt.Sprintf("%s@%d refused: %s (%s)", pack, version, r.act.GetReason(), strings.Join(subjects, ", ")),
			Detail:  map[string]any{"subjects": subjects, "server_message": r.message}})
	}
	return rt.contentError(err)
}

// --- history -----------------------------------------------------------------

func newContentHistoryCmd(rt *runtime) *cobra.Command {
	var limit uint32
	cmd := &cobra.Command{
		Use:           "history <pack>",
		Short:         "List a pack's versions: author, approval, and when each was active",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return rt.history(args[0], limit)
		},
	}
	cmd.Flags().Uint32Var(&limit, "limit", 0, "show only the newest N versions (0: all)")
	return cmd
}

type interval struct {
	From string `json:"from"`
	To   string `json:"to,omitempty"` // empty: still active
}

// activeIntervals turns the pointer moves, oldest first, into each version's
// spans in effect.
func activeIntervals(moves []*contentv1.ActiveVersion) map[uint64][]interval {
	out := map[uint64][]interval{}
	for i, m := range moves {
		iv := interval{From: stamp(m.GetActivatedAtUnixNano())}
		if i+1 < len(moves) {
			iv.To = stamp(moves[i+1].GetActivatedAtUnixNano())
		}
		out[m.GetVersion()] = append(out[m.GetVersion()], iv)
	}
	return out
}

func (rt *runtime) history(pack string, limit uint32) error {
	rt.commandAttrs(pack, 0)
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	resp, err := client.ListVersions(ctx, connect.NewRequest(&adminv1.ListVersionsRequest{PackId: pack, Limit: limit}))
	if err != nil {
		return rt.contentError(err)
	}
	active := resp.Msg.GetActiveVersion()
	spans := activeIntervals(resp.Msg.GetActivations())

	type row struct {
		Version     uint64     `json:"version"`
		Parent      uint64     `json:"parent_version"`
		Author      string     `json:"author"`
		Publisher   string     `json:"publisher"`
		PublishedAt string     `json:"published_at"`
		ApprovedBy  string     `json:"approved_by"`
		ApprovedAt  string     `json:"approved_at"`
		CoreVersion uint64     `json:"core_version"`
		Active      bool       `json:"active"`
		Intervals   []interval `json:"active_intervals"`
	}
	rows := make([]row, 0, len(resp.Msg.GetVersions()))
	for _, v := range resp.Msg.GetVersions() {
		ivs := spans[v.GetVersion()]
		if ivs == nil {
			ivs = []interval{}
		}
		rows = append(rows, row{
			Version: v.GetVersion(), Parent: v.GetParentVersion(), Author: v.GetAuthor(), Publisher: v.GetPublisher(),
			PublishedAt: stamp(v.GetPublishedAtUnixNano()), ApprovedBy: v.GetApprovedBy(),
			ApprovedAt: stamp(v.GetApprovedAtUnixNano()), CoreVersion: v.GetCoreVersion(),
			Active: v.GetVersion() == active, Intervals: ivs,
		})
	}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{"pack": pack, "active_version": active, "versions": rows, "trace_id": rt.traceID()})
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(rt.stdout, "%s has no published versions\n", pack)
		return err
	}
	for _, r := range rows {
		mark := " "
		if r.Active {
			mark = "*"
		}
		approval := "not approved"
		if r.ApprovedBy != "" {
			approval = fmt.Sprintf("approved by %s at %s", rt.who(r.ApprovedBy), r.ApprovedAt)
		}
		activeText := "never active"
		if len(r.Intervals) > 0 {
			parts := make([]string, 0, len(r.Intervals))
			for _, iv := range r.Intervals {
				to := iv.To
				if to == "" {
					to = "now"
				}
				parts = append(parts, iv.From+" to "+to)
			}
			activeText = "active " + strings.Join(parts, ", ")
		}
		fmt.Fprintf(rt.stdout, "%s %s@%d  published by %s at %s, %s; %s\n",
			mark, pack, r.Version, rt.who(r.Author), r.PublishedAt, approval, activeText)
	}
	return nil
}
