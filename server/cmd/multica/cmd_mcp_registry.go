package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var mcpRegistryCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage workspace MCP server registrations",
}

var mcpAddCmd = &cobra.Command{Use: "add", Short: "Register an MCP server", RunE: runMCPAdd}
var mcpListCmd = &cobra.Command{Use: "list", Short: "List MCP servers", RunE: runMCPList}
var mcpGetCmd = &cobra.Command{Use: "get <name-or-id>", Short: "Get MCP server details", Args: exactArgs(1), RunE: runMCPGet}
var mcpRemoveCmd = &cobra.Command{Use: "remove <name-or-id>", Short: "Remove an MCP server", Args: exactArgs(1), RunE: runMCPRemove}
var mcpSecretCmd = &cobra.Command{Use: "secret", Short: "Manage MCP server secrets"}
var mcpSecretSetCmd = &cobra.Command{Use: "set <name-or-id> <key>", Short: "Set an MCP server secret", Args: exactArgs(2), RunE: runMCPSecretSet}
var mcpSecretListCmd = &cobra.Command{Use: "list <name-or-id>", Short: "List MCP server secret keys", Args: exactArgs(1), RunE: runMCPSecretList}
var mcpSecretRmCmd = &cobra.Command{Use: "rm <name-or-id> <key>", Short: "Remove an MCP server secret", Args: exactArgs(2), RunE: runMCPSecretRm}
var mcpAllowCmd = &cobra.Command{Use: "allow <name-or-id>", Short: "Allow MCP tools for a server", Args: exactArgs(1), RunE: runMCPAllow}
var mcpDisallowCmd = &cobra.Command{Use: "disallow <name-or-id>", Short: "Remove MCP tools from a server allowlist", Args: exactArgs(1), RunE: runMCPDisallow}
var mcpTestCmd = &cobra.Command{Use: "test <name-or-id>", Short: "Test MCP server connectivity", Args: exactArgs(1), RunE: runMCPTest}
var mcpLogsCmd = &cobra.Command{Use: "logs", Short: "List MCP tool call logs", RunE: runMCPLogs}
var mcpRotateSecretCmd = &cobra.Command{Use: "rotate-secret <name-or-id>", Short: "Rotate an MCP server secret", Args: exactArgs(1), RunE: runMCPRotateSecret}
var mcpRevokeCmd = &cobra.Command{Use: "revoke <name-or-id>", Short: "Revoke stored OAuth tokens", Args: exactArgs(1), RunE: runMCPRevoke}
var mcpConnectCmd = &cobra.Command{Use: "connect <name-or-id>", Short: "Connect an OAuth MCP server", Args: exactArgs(1), RunE: runMCPConnect}

func init() {
	mcpRegistryCmd.AddCommand(mcpAddCmd, mcpListCmd, mcpGetCmd, mcpRemoveCmd, mcpSecretCmd, mcpAllowCmd, mcpDisallowCmd, mcpTestCmd, mcpLogsCmd, mcpRotateSecretCmd, mcpRevokeCmd, mcpConnectCmd)
	mcpSecretCmd.AddCommand(mcpSecretSetCmd, mcpSecretListCmd, mcpSecretRmCmd)

	mcpAddCmd.Flags().String("name", "", "Server name (required)")
	mcpAddCmd.Flags().String("transport", "", "Transport: stdio, sse, or http (required)")
	mcpAddCmd.Flags().String("url", "", "SSE/HTTP server URL")
	mcpAddCmd.Flags().String("command", "", "stdio command")
	mcpAddCmd.Flags().StringSlice("args", nil, "stdio command arguments")
	mcpAddCmd.Flags().String("scope", "workspace", "Scope: workspace or agent")
	mcpAddCmd.Flags().String("agent", "", "Agent name or ID for agent scope")
	mcpAddCmd.Flags().Bool("read-only", false, "Mark server as read-only")
	mcpAddCmd.Flags().Bool("required", false, "Fail runs if this server is unavailable")
	mcpAddCmd.Flags().String("approval-required", "none", "Approval required for: none or writes")
	mcpAddCmd.Flags().String("output", "json", "Output format: table or json")

	mcpListCmd.Flags().String("agent", "", "Show servers mounted for an agent")
	mcpListCmd.Flags().String("output", "table", "Output format: table or json")
	mcpListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
	mcpGetCmd.Flags().String("output", "json", "Output format: table or json")

	mcpSecretSetCmd.Flags().String("value", "", "Secret value (defaults to stdin)")
	mcpSecretListCmd.Flags().String("output", "table", "Output format: table or json")
	mcpAllowCmd.Flags().StringArray("tool", nil, "Tool name to allow (repeatable)")
	mcpDisallowCmd.Flags().StringArray("tool", nil, "Tool name to remove (repeatable)")
	mcpTestCmd.Flags().String("output", "json", "Output format: table or json")
	mcpLogsCmd.Flags().String("server", "", "Filter by server name")
	mcpLogsCmd.Flags().String("since", "", "Filter by date/time")
	mcpLogsCmd.Flags().String("output", "table", "Output format: table or json")
	mcpRotateSecretCmd.Flags().String("key", "", "Secret key to rotate (required)")
	mcpRotateSecretCmd.Flags().String("value", "", "Secret value (defaults to stdin)")
}

type mcpServerRef struct {
	ID      string
	Name    string
	Display string
}

func runMCPAdd(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	transport, _ := cmd.Flags().GetString("transport")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	if transport == "" {
		return fmt.Errorf("--transport is required")
	}
	scope, _ := cmd.Flags().GetString("scope")
	approval, _ := cmd.Flags().GetString("approval-required")
	body := map[string]any{
		"name":                  name,
		"transport":             transport,
		"scope":                 scope,
		"required":              mustGetBool(cmd, "required"),
		"read_only":             mustGetBool(cmd, "read-only"),
		"approval_required_for": approval,
	}
	if v, _ := cmd.Flags().GetString("url"); v != "" {
		body["url"] = v
	}
	if v, _ := cmd.Flags().GetString("command"); v != "" {
		body["command"] = v
	}
	if args, _ := cmd.Flags().GetStringSlice("args"); len(args) > 0 {
		body["args"] = args
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if agent, _ := cmd.Flags().GetString("agent"); agent != "" {
		agentID, err := resolveAgent(ctx, client, agent)
		if err != nil {
			return fmt.Errorf("resolve agent: %w", err)
		}
		body["agent_id"] = agentID
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/mcp-servers", body, &result); err != nil {
		return fmt.Errorf("add mcp server: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("MCP server added: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runMCPList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var agentID string
	if agent, _ := cmd.Flags().GetString("agent"); agent != "" {
		agentID, err = resolveAgent(ctx, client, agent)
		if err != nil {
			return fmt.Errorf("resolve agent: %w", err)
		}
	}
	var resp struct {
		MCPServers []map[string]any `json:"mcp_servers"`
		Total      int              `json:"total"`
	}
	if err := client.GetJSON(ctx, "/api/mcp-servers", &resp); err != nil {
		return fmt.Errorf("list mcp servers: %w", err)
	}
	if agentID != "" {
		filtered := resp.MCPServers[:0]
		for _, s := range resp.MCPServers {
			if strVal(s, "scope") == "workspace" || strVal(s, "agent_id") == agentID {
				filtered = append(filtered, s)
			}
		}
		resp.MCPServers = filtered
		resp.Total = len(filtered)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"ID", "NAME", "TRANSPORT", "SCOPE", "REQUIRED", "READ_ONLY"}
	rows := make([][]string, 0, len(resp.MCPServers))
	for _, s := range resp.MCPServers {
		rows = append(rows, []string{
			displayID(strVal(s, "id"), fullID),
			strVal(s, "name"),
			strVal(s, "transport"),
			strVal(s, "scope"),
			strVal(s, "required"),
			strVal(s, "read_only"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runMCPGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	var resp map[string]any
	if err := client.GetJSON(ctx, "/api/mcp-servers/"+ref.ID, &resp); err != nil {
		return fmt.Errorf("get mcp server: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	server, _ := resp["mcp_server"].(map[string]any)
	cli.PrintTable(os.Stdout, []string{"ID", "NAME", "TRANSPORT", "SCOPE", "REQUIRED", "READ_ONLY"}, [][]string{{
		strVal(server, "id"), strVal(server, "name"), strVal(server, "transport"), strVal(server, "scope"), strVal(server, "required"), strVal(server, "read_only"),
	}})
	return nil
}

func runMCPRemove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	if err := client.DeleteJSON(ctx, "/api/mcp-servers/"+ref.ID); err != nil {
		return fmt.Errorf("remove mcp server: %w", err)
	}
	fmt.Printf("MCP server %s removed.\n", ref.Display)
	return nil
}

func runMCPSecretSet(cmd *cobra.Command, args []string) error {
	return setMCPSecret(cmd, args[0], args[1])
}

func runMCPRotateSecret(cmd *cobra.Command, args []string) error {
	key, _ := cmd.Flags().GetString("key")
	if key == "" {
		return fmt.Errorf("--key is required")
	}
	return setMCPSecret(cmd, args[0], key)
}

func setMCPSecret(cmd *cobra.Command, serverRef, key string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, serverRef)
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	value, _ := cmd.Flags().GetString("value")
	if value == "" && !cmd.Flags().Changed("value") {
		info, statErr := os.Stdin.Stat()
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("--value is required when stdin is not piped")
		}
		b, readErr := io.ReadAll(os.Stdin)
		if readErr != nil {
			return readErr
		}
		value = strings.TrimRight(string(b), "\r\n")
	}
	var resp map[string]any
	if err := client.PutJSON(ctx, "/api/mcp-servers/"+ref.ID+"/secrets/"+url.PathEscape(key), map[string]any{"value": value}, &resp); err != nil {
		return fmt.Errorf("set mcp secret: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runMCPSecretList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	var resp struct {
		Secrets []map[string]any `json:"secrets"`
		Total   int              `json:"total"`
	}
	if err := client.GetJSON(ctx, "/api/mcp-servers/"+ref.ID+"/secrets", &resp); err != nil {
		return fmt.Errorf("list mcp secrets: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	rows := make([][]string, 0, len(resp.Secrets))
	for _, s := range resp.Secrets {
		rows = append(rows, []string{strVal(s, "key"), strVal(s, "updated_at")})
	}
	cli.PrintTable(os.Stdout, []string{"KEY", "UPDATED_AT"}, rows)
	return nil
}

func runMCPSecretRm(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	if err := client.DeleteJSON(ctx, "/api/mcp-servers/"+ref.ID+"/secrets/"+url.PathEscape(args[1])); err != nil {
		return fmt.Errorf("remove mcp secret: %w", err)
	}
	fmt.Printf("MCP secret %s removed.\n", args[1])
	return nil
}

func runMCPAllow(cmd *cobra.Command, args []string) error {
	return mutateMCPAllowlist(cmd, args[0], true)
}

func runMCPDisallow(cmd *cobra.Command, args []string) error {
	return mutateMCPAllowlist(cmd, args[0], false)
}

func mutateMCPAllowlist(cmd *cobra.Command, serverRef string, add bool) error {
	tools, _ := cmd.Flags().GetStringArray("tool")
	if len(tools) == 0 {
		return fmt.Errorf("--tool is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, serverRef)
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	for _, tool := range tools {
		if add {
			var resp map[string]any
			if err := client.PostJSON(ctx, "/api/mcp-servers/"+ref.ID+"/allowlist", map[string]any{"tool_name": tool}, &resp); err != nil {
				return fmt.Errorf("allow mcp tool %s: %w", tool, err)
			}
		} else {
			if err := client.DeleteJSON(ctx, "/api/mcp-servers/"+ref.ID+"/allowlist/"+url.PathEscape(tool)); err != nil {
				return fmt.Errorf("disallow mcp tool %s: %w", tool, err)
			}
		}
	}
	fmt.Printf("MCP allowlist updated for %s.\n", ref.Display)
	return nil
}

func runMCPTest(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/mcp-servers/"+ref.ID+"/test", nil, &resp); err != nil {
		return fmt.Errorf("test mcp server: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runMCPLogs(cmd *cobra.Command, _ []string) error {
	return fmt.Errorf("mcp logs is not implemented until Phase 3 audit logging")
}

func runMCPRevoke(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ref, err := resolveMCPServerID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve mcp server: %w", err)
	}
	if err := client.DeleteJSON(ctx, "/api/mcp-servers/"+ref.ID+"/secrets/_oauth_refresh_token"); err != nil {
		return fmt.Errorf("revoke mcp oauth token: %w", err)
	}
	fmt.Printf("MCP OAuth token revoked for %s.\n", ref.Display)
	return nil
}

func runMCPConnect(cmd *cobra.Command, _ []string) error {
	return fmt.Errorf("mcp connect is not implemented until Phase 4 OAuth lifecycle")
}

func resolveMCPServerID(ctx context.Context, client *cli.APIClient, nameOrID string) (mcpServerRef, error) {
	if uuidRegexp.MatchString(nameOrID) {
		return mcpServerRef{ID: nameOrID, Display: nameOrID}, nil
	}
	if client.WorkspaceID == "" {
		return mcpServerRef{}, fmt.Errorf("workspace ID is required to resolve MCP servers; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}
	var resp struct {
		MCPServers []map[string]any `json:"mcp_servers"`
	}
	path := "/api/mcp-servers?" + url.Values{"name": {nameOrID}}.Encode()
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return mcpServerRef{}, fmt.Errorf("fetch mcp servers: %w", err)
	}
	if len(resp.MCPServers) == 0 {
		return mcpServerRef{}, fmt.Errorf("no MCP server found matching %q", nameOrID)
	}
	if len(resp.MCPServers) > 1 {
		return mcpServerRef{}, fmt.Errorf("ambiguous MCP server %q", nameOrID)
	}
	server := resp.MCPServers[0]
	name := strVal(server, "name")
	id := strVal(server, "id")
	return mcpServerRef{ID: id, Name: name, Display: fmt.Sprintf("%s (%s)", name, truncateID(id))}, nil
}

func mustGetBool(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}
