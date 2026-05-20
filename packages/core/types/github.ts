export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

export interface GitHubInstallation {
  id: string;
  workspace_id: string;
  /** GitHub's numeric installation id — the management handle used by the
   * connect / disconnect flows. Omitted when the caller cannot manage
   * integrations (see `ListGitHubInstallationsResponse.can_manage`). */
  installation_id?: number;
  account_login: string;
  account_type: "User" | "Organization";
  account_avatar_url: string | null;
  created_at: string;
  /** Display name of the workspace member who connected this installation.
   * Optional because older backends and minimum-visibility deployments may
   * omit it; the UI renders the "connected by" line only when present. */
  connected_by?: string;
}

export interface GitHubPullRequest {
  id: string;
  workspace_id: string;
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  branch: string | null;
  author_login: string | null;
  author_avatar_url: string | null;
  merged_at: string | null;
  closed_at: string | null;
  pr_created_at: string;
  pr_updated_at: string;
}

export interface ListGitHubInstallationsResponse {
  installations: GitHubInstallation[];
  /** Whether the deployment has GitHub App credentials configured. When false, the Connect button is hidden / disabled. */
  configured: boolean;
  /** Whether the caller can connect / disconnect installations. Non-admin
   * members get `false` along with installations that omit `installation_id`.
   * Older backends predating MUL-2413 omit the field; treat absence as
   * `false` for read-only safety. */
  can_manage?: boolean;
}

export interface GitHubConnectResponse {
  /** The GitHub App install URL the browser should open. Empty when `configured` is false. */
  url?: string;
  configured: boolean;
}
