package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// Draft commands — the agent-capability surface over the Drafts REST API.
// Each subcommand wraps an existing Drafts handler; there is NO new business
// logic here. This is what lets Aye (the lead agent) read a draft + its
// conversation rail + its open-annotation queue and respond: converse in the
// rail, reply to threads, set annotation state, and plant anchored
// question-threads / suggestions.
//
// When the CLI runs inside a daemon task the API client carries X-Agent-ID +
// X-Task-ID (set from MULTICA_AGENT_ID / MULTICA_TASK_ID by newAPIClient), so
// the server attributes every rail/annotation write as author_type='agent'. No
// flag is needed — the attribution is derived from the trusted task headers.
//
// Two write channels, one principle — talk lands in the rail; structure lands
// in the doc:
//   - `say` / `messages` — the conversation rail: general talk (narration,
//     alignment, direction questions, acknowledgements), UN-anchored.
//   - `reply` / `annotate` — anchored annotations: points tied to exact text.
//
// There is deliberately no `draft set-body` command — the body endpoint is a
// full-replace with no CRDT, so wholesale rewriting is out of scope until the
// co-editing slice.
// ---------------------------------------------------------------------------

var draftCmd = &cobra.Command{
	Use:   "draft",
	Short: "Read and annotate Drafts (agent co-authoring surface)",
	Long: `Drafts are living markdown documents a human co-authors with the lead
agent. This command group is the agent's read/write surface over a draft:
fetch the body + metadata, read + post to the conversation rail, list the
open-annotation queue with its threads, reply to a thread, transition an
annotation's state, and plant new anchored annotations (questions / suggestions).

Talk lands in the rail (say / messages); structure lands in the doc (reply /
annotate). It wraps the existing Drafts REST API — there is no separate business
logic. When invoked inside a draft-turn task the writes are attributed to the
agent (author_type='agent'); the body itself is never rewritten here (suggest
via annotations instead).`,
}

var draftGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a draft's body + metadata",
	Args:  exactArgs(1),
	RunE:  runDraftGet,
}

var draftAnnotationsCmd = &cobra.Command{
	Use:   "annotations <id>",
	Short: "List a draft's annotations and their threads (the open queue)",
	Long: `Lists every annotation on a draft with its full thread. Pass
--state open to scope to the work queue (threads still needing attention).
Each annotation carries its anchor (quote + surrounding context), type,
state, and the back-and-forth messages — this is the board Aye drains.`,
	Args: exactArgs(1),
	RunE: runDraftAnnotations,
}

var draftReplyCmd = &cobra.Command{
	Use:   "reply <id> <annotationId>",
	Short: "Add a message to an annotation thread",
	Args:  exactArgs(2),
	RunE:  runDraftReply,
}

var draftMessagesCmd = &cobra.Command{
	Use:   "messages <id>",
	Short: "List the draft's conversation rail (the germination conversation)",
	Long: `Lists every message on a draft's conversation rail, oldest-first. The
rail is the draft-level, UN-anchored back-and-forth alongside the document —
narration, alignment, and direction talk — distinct from the anchored
annotation threads (see ` + "`annotations`" + `). Read it at turn start to
understand what the human wants.`,
	Args: exactArgs(1),
	RunE: runDraftMessages,
}

var draftSayCmd = &cobra.Command{
	Use:   "say <id>",
	Short: "Post a message to the draft's conversation rail",
	Long: `Posts a conversation message to a draft's rail. This is where GENERAL
talk lands — narration, alignment, questions about direction, acknowledgements,
"here's my thinking" — anything not anchored to a specific text span. For points
anchored to exact text, reserve annotations (` + "`reply`" + ` / ` + "`annotate`" + `)
instead. Talk lands in the rail; structure lands in the doc.

When invoked inside a draft-turn task the message is attributed to the agent
(author_type='agent') via the trusted task headers — no flag is needed.`,
	Args: exactArgs(1),
	RunE: runDraftSay,
}

var draftResolveCmd = &cobra.Command{
	Use:   "resolve <id> <annotationId>",
	Short: "Mark an annotation thread resolved (settled)",
	Args:  exactArgs(2),
	RunE:  runDraftSetState("resolved"),
}

var draftAckCmd = &cobra.Command{
	Use:   "ack <id> <annotationId>",
	Short: "Acknowledge an annotation (seen, not yet settled)",
	Args:  exactArgs(2),
	RunE:  runDraftSetState("acknowledged"),
}

var draftDismissCmd = &cobra.Command{
	Use:   "dismiss <id> <annotationId>",
	Short: "Dismiss an annotation thread",
	Args:  exactArgs(2),
	RunE:  runDraftSetState("dismissed"),
}

var draftAnnotateCmd = &cobra.Command{
	Use:   "annotate <id>",
	Short: "Create an annotation (open a question thread or plant a suggestion)",
	Long: `Creates a new anchored annotation on a draft. Aye uses this to plant
question-threads precisely on the text they're about ("§4 — should this cover
rollback?") and to propose suggestions (a before→after the human accepts in
one click).

Anchor the annotation with --quote (the exact text it's about). For a
suggestion, pass --type suggestion plus --suggest-after (the replacement) and
optionally --suggest-before (the original, defaults to --quote). For a
question/comment, pass --type question (or comment) and --body (the opening
message).`,
	Args: exactArgs(1),
	RunE: runDraftAnnotate,
}

func init() {
	draftCmd.AddCommand(draftGetCmd)
	draftCmd.AddCommand(draftAnnotationsCmd)
	draftCmd.AddCommand(draftMessagesCmd)
	draftCmd.AddCommand(draftSayCmd)
	draftCmd.AddCommand(draftReplyCmd)
	draftCmd.AddCommand(draftResolveCmd)
	draftCmd.AddCommand(draftAckCmd)
	draftCmd.AddCommand(draftDismissCmd)
	draftCmd.AddCommand(draftAnnotateCmd)

	draftGetCmd.Flags().String("output", "json", "Output format: table or json")

	draftAnnotationsCmd.Flags().String("state", "", "Filter by state: open | acknowledged | resolved | dismissed (default: all)")
	draftAnnotationsCmd.Flags().String("output", "json", "Output format: table or json")

	draftMessagesCmd.Flags().String("output", "json", "Output format: table or json")

	draftSayCmd.Flags().String("body", "", "Message body (required)")
	draftSayCmd.Flags().String("output", "json", "Output format: table or json")

	draftReplyCmd.Flags().String("body", "", "Reply body (required)")
	draftReplyCmd.Flags().String("output", "json", "Output format: table or json")

	for _, c := range []*cobra.Command{draftResolveCmd, draftAckCmd, draftDismissCmd} {
		c.Flags().String("output", "json", "Output format: table or json")
	}

	draftAnnotateCmd.Flags().String("quote", "", "Exact text the annotation anchors to (required)")
	draftAnnotateCmd.Flags().String("type", "question", "Annotation type: question | comment | suggestion")
	draftAnnotateCmd.Flags().String("body", "", "Opening thread message (for question/comment)")
	draftAnnotateCmd.Flags().String("context-before", "", "Text immediately before the quote (improves re-anchoring)")
	draftAnnotateCmd.Flags().String("context-after", "", "Text immediately after the quote (improves re-anchoring)")
	draftAnnotateCmd.Flags().Int("pos-hint", 0, "Character-offset hint for the anchor")
	draftAnnotateCmd.Flags().String("suggest-before", "", "Original text for a suggestion (defaults to --quote)")
	draftAnnotateCmd.Flags().String("suggest-after", "", "Replacement text for a suggestion")
	draftAnnotateCmd.Flags().String("output", "json", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// summarizeAnnotationRow renders one table row for the annotations list.
func summarizeAnnotationRow(m map[string]any) []string {
	quote := strVal(m, "quote")
	if len(quote) > 48 {
		quote = quote[:45] + "..."
	}
	threadLen := 0
	if msgs, ok := m["messages"].([]any); ok {
		threadLen = len(msgs)
	}
	return []string{
		truncateID(strVal(m, "id")),
		strVal(m, "type"),
		strVal(m, "state"),
		strVal(m, "author_type"),
		quote,
		fmt.Sprintf("%d", threadLen),
	}
}

// summarizeMessageRow renders one table row for the conversation-rail list.
func summarizeMessageRow(m map[string]any) []string {
	body := strVal(m, "body")
	if len(body) > 60 {
		body = body[:57] + "..."
	}
	created := strVal(m, "created_at")
	if len(created) >= 19 {
		created = created[:19]
	}
	return []string{
		truncateID(strVal(m, "id")),
		strVal(m, "author_type"),
		body,
		created,
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func runDraftGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var draft map[string]any
	if err := client.GetJSON(ctx, "/api/drafts/"+args[0], &draft); err != nil {
		return fmt.Errorf("get draft: %w", err)
	}
	// `get` always returns JSON — the consumer wants the full body, not a
	// one-line summary.
	return cli.PrintJSON(os.Stdout, draft)
}

func runDraftAnnotations(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/drafts/"+args[0]+"/annotations", &result); err != nil {
		return fmt.Errorf("list draft annotations: %w", err)
	}

	// The list endpoint returns every annotation; --state is a client-side
	// filter so the agent can scope to its work queue without a new endpoint.
	wantState, _ := cmd.Flags().GetString("state")
	if wantState != "" {
		if listRaw, ok := result["annotations"].([]any); ok {
			filtered := make([]any, 0, len(listRaw))
			for _, raw := range listRaw {
				if m, ok := raw.(map[string]any); ok && strVal(m, "state") == wantState {
					filtered = append(filtered, raw)
				}
			}
			result["annotations"] = filtered
			result["total"] = len(filtered)
		}
	}

	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, result)
	}
	listRaw, _ := result["annotations"].([]any)
	headers := []string{"ID", "TYPE", "STATE", "AUTHOR", "QUOTE", "THREAD"}
	rows := make([][]string, 0, len(listRaw))
	for _, raw := range listRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, summarizeAnnotationRow(m))
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runDraftReply(cmd *cobra.Command, args []string) error {
	body, _ := cmd.Flags().GetString("body")
	if body == "" {
		return fmt.Errorf("--body is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	path := fmt.Sprintf("/api/drafts/%s/annotations/%s/messages", args[0], url.PathEscape(args[1]))
	var msg map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{"body": body}, &msg); err != nil {
		return fmt.Errorf("reply to annotation: %w", err)
	}
	return cli.PrintJSON(os.Stdout, msg)
}

func runDraftMessages(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/drafts/"+args[0]+"/messages", &result); err != nil {
		return fmt.Errorf("list draft messages: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, result)
	}
	listRaw, _ := result["messages"].([]any)
	headers := []string{"ID", "AUTHOR", "BODY", "CREATED"}
	rows := make([][]string, 0, len(listRaw))
	for _, raw := range listRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, summarizeMessageRow(m))
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runDraftSay(cmd *cobra.Command, args []string) error {
	body, _ := cmd.Flags().GetString("body")
	if body == "" {
		return fmt.Errorf("--body is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var msg map[string]any
	if err := client.PostJSON(ctx, "/api/drafts/"+args[0]+"/messages", map[string]any{"body": body}, &msg); err != nil {
		return fmt.Errorf("post draft message: %w", err)
	}
	return cli.PrintJSON(os.Stdout, msg)
}

// runDraftSetState builds a RunE that PATCHes an annotation to a fixed state.
// The three state verbs (resolve/ack/dismiss) share this body; the state value
// is bound at command-construction time.
func runDraftSetState(state string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		path := fmt.Sprintf("/api/drafts/%s/annotations/%s", args[0], url.PathEscape(args[1]))
		var ann map[string]any
		if err := client.PatchJSON(ctx, path, map[string]any{"state": state}, &ann); err != nil {
			return fmt.Errorf("set annotation state to %s: %w", state, err)
		}
		return cli.PrintJSON(os.Stdout, ann)
	}
}

func runDraftAnnotate(cmd *cobra.Command, args []string) error {
	quote, _ := cmd.Flags().GetString("quote")
	if quote == "" {
		return fmt.Errorf("--quote is required (the exact text to anchor to)")
	}
	annType, _ := cmd.Flags().GetString("type")
	body, _ := cmd.Flags().GetString("body")
	contextBefore, _ := cmd.Flags().GetString("context-before")
	contextAfter, _ := cmd.Flags().GetString("context-after")
	posHint, _ := cmd.Flags().GetInt("pos-hint")
	suggestBefore, _ := cmd.Flags().GetString("suggest-before")
	suggestAfter, _ := cmd.Flags().GetString("suggest-after")

	reqBody := map[string]any{
		"type":           annType,
		"quote":          quote,
		"context_before": contextBefore,
		"context_after":  contextAfter,
		"pos_hint":       posHint,
	}
	if body != "" {
		reqBody["message"] = body
	}
	if annType == "suggestion" {
		if suggestAfter == "" {
			return fmt.Errorf("--suggest-after is required when --type is suggestion")
		}
		before := suggestBefore
		if before == "" {
			before = quote
		}
		reqBody["suggestion_before"] = before
		reqBody["suggestion_after"] = suggestAfter
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var ann map[string]any
	if err := client.PostJSON(ctx, "/api/drafts/"+args[0]+"/annotations", reqBody, &ann); err != nil {
		return fmt.Errorf("create annotation: %w", err)
	}
	return cli.PrintJSON(os.Stdout, ann)
}
