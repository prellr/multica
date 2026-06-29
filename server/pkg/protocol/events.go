package protocol

// Event types for WebSocket communication between server, web clients, and daemon.
const (
	// Issue events
	EventIssueCreated         = "issue:created"
	EventIssueUpdated         = "issue:updated"
	EventIssueDeleted         = "issue:deleted"
	EventIssueMetadataChanged = "issue_metadata:changed"

	// Comment events
	EventCommentCreated       = "comment:created"
	EventCommentBroadcast     = "comment:broadcast"
	EventCommentUpdated       = "comment:updated"
	EventCommentDeleted       = "comment:deleted"
	EventCommentResolved      = "comment:resolved"
	EventCommentUnresolved    = "comment:unresolved"
	EventReactionAdded        = "reaction:added"
	EventReactionRemoved      = "reaction:removed"
	EventIssueReactionAdded   = "issue_reaction:added"
	EventIssueReactionRemoved = "issue_reaction:removed"

	// Agent events
	EventAgentStatus   = "agent:status"
	EventAgentCreated  = "agent:created"
	EventAgentArchived = "agent:archived"
	EventAgentRestored = "agent:restored"

	// Agent task queue events (server <-> daemon).
	// Each event maps to a status transition on agent_task_queue. These are
	// the *agent execution lifecycle* — distinct from the user-facing task
	// CRUD signals below (EventUserTask*), which fire on the issue table's
	// kind='task' rows. The frontend subscribes to specific event names by
	// exact match, so the shared `task:` prefix does not cause routing
	// collisions, but readers comparing the two blocks should not conflate
	// them: agent-task verbs are status transitions (queued, dispatch,
	// running, ...); user-task verbs are CRUD (created, updated, deleted).
	EventTaskQueued    = "task:queued"   // ∅ → queued (enqueue / retry create)
	EventTaskDispatch  = "task:dispatch" // queued → dispatched (daemon claim)
	EventTaskRunning   = "task:running"  // dispatched → running (daemon started)
	EventTaskProgress  = "task:progress"
	EventTaskCompleted = "task:completed" // running → completed
	EventTaskFailed    = "task:failed"    // running → failed
	EventTaskMessage   = "task:message"
	EventTaskCancelled = "task:cancelled" // * → cancelled

	// User task events (CRUD on issue rows where kind='task'). See the
	// agent-task block above for why these share the `task:` prefix without
	// colliding — frontend subscribes to exact event names.
	EventUserTaskCreated = "task:created"
	EventUserTaskUpdated = "task:updated"
	EventUserTaskDeleted = "task:deleted"
	// Promote-to-issue lifecycle. Payload: { task_id, issue } so a single
	// event drives both cache transitions on the receiver — the task list
	// removes the row, the issue list adds it. Firing task:deleted +
	// issue:created separately would race (a client receiving only one
	// half mid-network-blip would render an inconsistent view); the
	// combined event keeps the transition atomic.
	EventUserTaskPromoted = "task:promoted"

	// Draft events (CRUD on the standalone draft table). Workspace- and
	// owner-scoped; the frontend invalidates the draft list/detail caches on
	// receipt so a draft created/edited/deleted in one client (or, in a later
	// slice, by the agent) stays fresh everywhere. Created/updated carry the
	// full draft payload; deleted carries only `draft_id` (the receiver removes
	// it from the list and clears its detail cache).
	EventDraftCreated = "draft:created"
	EventDraftUpdated = "draft:updated"
	EventDraftDeleted = "draft:deleted"

	// Draft annotation events (Drafts slice 1 — the non-destructive annotation
	// overlay on a draft body). Workspace-scoped like drafts. Created/updated
	// carry the full annotation payload (anchor + state + thread), deleted
	// carries `{ draft_id, annotation_id }` so the receiver can drop it from
	// the right draft's annotation cache. message:created carries the new
	// thread message plus its draft+annotation ids. Slice 1 is human-only;
	// slice 2 (agent authoring) reuses these same events unchanged.
	EventDraftAnnotationCreated        = "draft_annotation:created"
	EventDraftAnnotationUpdated        = "draft_annotation:updated"
	EventDraftAnnotationDeleted        = "draft_annotation:deleted"
	EventDraftAnnotationMessageCreated = "draft_annotation:message_created"

	// Draft turn lifecycle (Drafts slice 2 — the Send-turn). Emitted when Aye's
	// draft-turn task completes: carries `{ draft_id, task_id, agent_id,
	// summary }`. The agent's per-turn replies + suggestions stream in live as
	// draft_annotation:* events during the run; this single completion event is
	// what lets the Draft view dismiss the "Aye is working" indicator and show
	// her closing narration. The granular thinking/tool stream is the existing
	// task:message / task:progress events, scoped by task_id.
	EventDraftTurnCompleted = "draft:turn_completed"

	// Inbox events
	EventInboxNew           = "inbox:new"
	EventInboxRead          = "inbox:read"
	EventInboxArchived      = "inbox:archived"
	EventInboxBatchRead     = "inbox:batch-read"
	EventInboxBatchArchived = "inbox:batch-archived"

	// Workspace events
	EventWorkspaceUpdated = "workspace:updated"
	EventWorkspaceDeleted = "workspace:deleted"

	// Member events
	EventMemberAdded   = "member:added"
	EventMemberUpdated = "member:updated"
	EventMemberRemoved = "member:removed"

	// Subscriber events
	EventSubscriberAdded   = "subscriber:added"
	EventSubscriberRemoved = "subscriber:removed"

	// Activity events
	EventActivityCreated = "activity:created"

	// Skill events
	EventSkillCreated = "skill:created"
	EventSkillUpdated = "skill:updated"
	EventSkillDeleted = "skill:deleted"

	// Chat events
	EventChatMessage        = "chat:message"
	EventChatDone           = "chat:done"
	EventChatSessionRead    = "chat:session_read"
	EventChatSessionDeleted = "chat:session_deleted"
	EventChatSessionUpdated = "chat:session_updated"

	// Channel events. Channels is the multi-participant chat surface (distinct
	// from the 1:1 agent Chat above) — see migration 065 and the channels
	// feature spec. Frontend subscribes by `channel:` prefix and invalidates
	// the per-channel message cache.
	EventChannelCreated       = "channel:created"
	EventChannelUpdated       = "channel:updated"
	EventChannelArchived      = "channel:archived"
	EventChannelMessage       = "channel:message"
	EventChannelMemberAdded   = "channel:member_added"
	EventChannelMemberRemoved = "channel:member_removed"
	EventChannelRead          = "channel:read"
	// Phase 5 — author-driven edits / soft-deletes propagate via these
	// per-message events. Distinct from channel:message (the new-message
	// signal) so the frontend can patch the cache surgically rather than
	// invalidate the whole timeline.
	EventChannelMessageUpdated = "channel:message_updated"
	EventChannelMessageDeleted = "channel:message_deleted"
	// Channel-message reactions (Phase 4). Separate from EventReactionAdded
	// (which targets issue comments) and EventIssueReactionAdded (issues)
	// because the payload shape differs — channel reactions carry channel_id
	// + channel_message_id rather than issue_id + comment_id, and grouping
	// the three under one event would force the frontend to discriminate
	// on payload keys.
	EventChannelReactionAdded   = "channel_reaction:added"
	EventChannelReactionRemoved = "channel_reaction:removed"

	// Project events
	EventProjectCreated         = "project:created"
	EventProjectUpdated         = "project:updated"
	EventProjectDeleted         = "project:deleted"
	EventProjectResourceCreated = "project_resource:created"
	EventProjectResourceDeleted = "project_resource:deleted"

	// Memory artifact events — workspace-scoped knowledge primitives
	// (wiki pages, agent notes, runbooks, decision logs).
	// Archive / restore reuse the Updated event so clients can re-render
	// based on the artifact's archived_at field instead of needing a
	// dedicated wire signal.
	EventMemoryArtifactCreated = "memory_artifact:created"
	EventMemoryArtifactUpdated = "memory_artifact:updated"
	EventMemoryArtifactDeleted = "memory_artifact:deleted"

	// Label events
	EventLabelCreated       = "label:created"
	EventLabelUpdated       = "label:updated"
	EventLabelDeleted       = "label:deleted"
	EventIssueLabelsChanged = "issue_labels:changed"

	// Pin events
	EventPinCreated   = "pin:created"
	EventPinDeleted   = "pin:deleted"
	EventPinReordered = "pin:reordered"

	// Invitation events
	EventInvitationCreated  = "invitation:created"
	EventInvitationAccepted = "invitation:accepted"
	EventInvitationDeclined = "invitation:declined"
	EventInvitationRevoked  = "invitation:revoked"

	// Autopilot events
	EventAutopilotCreated  = "autopilot:created"
	EventAutopilotUpdated  = "autopilot:updated"
	EventAutopilotDeleted  = "autopilot:deleted"
	EventAutopilotRunStart = "autopilot:run_start"
	EventAutopilotRunDone  = "autopilot:run_done"

	// Ship Hub events. Phase 1 publishes at the workspace scope; the
	// frontend invalidates the per-project PR/deploy queries on receipt.
	// Phase 2 adds the granular webhook-driven signals — frontends that
	// only care about coarse refreshes can keep listening to
	// pull_request:synced; those that want surgical cache patches subscribe
	// to the per-PR / per-deploy events.
	EventPullRequestSynced       = "pull_request:synced"
	EventPullRequestStateChanged = "pull_request:state_changed"
	EventDeployStarted           = "deploy:started"
	EventDeployProgress          = "deploy:progress"
	EventDeployCompleted         = "deploy:completed"
	// Phase 3 — card-action audit signal. Fired after every chip press
	// regardless of synchronous/async outcome so the frontend can update
	// the card's "recent actions" footer in real time.
	EventCardAction = "ship:card_action"

	// Phase 7a — Release lifecycle signals. created/updated/cancelled
	// mirror the three CRUD verbs the dialog and detail page exercise;
	// later phases (7b/c/d) reuse `:updated` for stage transitions
	// rather than minting a separate event per stage. Payload always
	// carries `release_id`; `created` also carries `project_id` and
	// `stage` so the rail can decide whether to insert; `updated`
	// carries `stage` so the cache can patch in place; `cancelled`
	// carries only `release_id` (the receiver removes from the rail).
	EventReleaseCreated   = "release:created"
	EventReleaseUpdated   = "release:updated"
	EventReleaseCancelled = "release:cancelled"

	// Phase 7b — merge train signals. The orchestrator emits these
	// while a release is in stage=merging so the detail page can
	// render live progress without polling. Payload conventions:
	//   merge_started    — { release_id, total_prs }
	//   merge_progress   — { release_id, pr_id, merge_state,
	//                        merged_count, total }
	//   merge_paused     — { release_id, paused_pr_id, error }
	//   merge_completed  — { release_id }
	//   merge_aborted    — { release_id, reason }
	// EventReleaseUpdated still fires on stage transitions; these
	// add the merge-specific granularity on top.
	EventReleaseMergeStarted   = "release:merge_started"
	EventReleaseMergeProgress  = "release:merge_progress"
	EventReleaseMergePaused    = "release:merge_paused"
	EventReleaseMergeCompleted = "release:merge_completed"
	EventReleaseMergeAborted   = "release:merge_aborted"

	// Phase 7c — staging-stage signals. The deployment_status webhook
	// fires release:staging_landed when a deploy whose sha matches
	// the release's merged_main_sha lands successfully; check_run
	// completion fires release:smoke_updated; the manual verify gate
	// fires release:verified / release:unverified. Payload conventions:
	//   staging_landed   — { release_id, deploy_id, sha, smoke_status }
	//   smoke_updated    — { release_id, smoke_status }
	//   verified         — { release_id, verified_by, verified_at }
	//   unverified       — { release_id, reason }
	EventReleaseStagingLanded = "release:staging_landed"
	EventReleaseSmokeUpdated  = "release:smoke_updated"
	EventReleaseVerified      = "release:verified"
	EventReleaseUnverified    = "release:unverified"

	// Phase 7d — production-stage signals. The user clicks Promote
	// (release:promoted), the deploy webhook auto-links the production
	// deploy (release:in_production), the user clicks Rollback
	// (release:rollback_initiated), or the health monitor recomputes
	// the rollup (release:health_updated). Payload conventions:
	//   promoted             — { release_id, sha, by_user_id }
	//   in_production        — { release_id, deploy_id, sha }
	//   rollback_initiated   — { release_id, reason, by_user_id }
	//   health_updated       — { release_id, overall_status }
	EventReleasePromoted          = "release:promoted"
	EventReleaseInProduction      = "release:in_production"
	EventReleaseRollbackInitiated = "release:rollback_initiated"
	EventReleaseHealthUpdated     = "release:health_updated"

	// Squad events
	EventSquadCreated = "squad:created"
	EventSquadUpdated = "squad:updated"
	EventSquadDeleted = "squad:deleted"

	// Daemon events
	EventDaemonHeartbeat     = "daemon:heartbeat"
	EventDaemonHeartbeatAck  = "daemon:heartbeat_ack"
	EventDaemonRegister      = "daemon:register"
	EventDaemonTaskAvailable = "daemon:task_available"

	// GitHub integration events
	EventGitHubInstallationCreated = "github_installation:created"
	EventGitHubInstallationDeleted = "github_installation:deleted"
	EventPullRequestLinked         = "pull_request:linked"
	EventPullRequestUpdated        = "pull_request:updated"
	EventPullRequestUnlinked       = "pull_request:unlinked"
)
