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

var reminderCmd = &cobra.Command{
	Use:   "reminder",
	Short: "Manage reminders",
}

var reminderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List reminders in the workspace",
	RunE:  runReminderList,
}

var reminderCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a reminder",
	RunE:  runReminderCreate,
}

var reminderGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get reminder details",
	Args:  exactArgs(1),
	RunE:  runReminderGet,
}

var reminderCancelCmd = &cobra.Command{
	Use:   "cancel <id>",
	Short: "Cancel a pending reminder",
	Args:  exactArgs(1),
	RunE:  runReminderCancel,
}

func init() {
	reminderCmd.AddCommand(reminderListCmd)
	reminderCmd.AddCommand(reminderCreateCmd)
	reminderCmd.AddCommand(reminderGetCmd)
	reminderCmd.AddCommand(reminderCancelCmd)

	reminderListCmd.Flags().String("status", "", "Filter by status (pending, delivered, cancelled)")
	reminderListCmd.Flags().String("kind", "", "Filter by kind (system, task, check_in)")
	reminderListCmd.Flags().String("recipient-type", "", "Filter by recipient type (member, agent)")
	reminderListCmd.Flags().String("recipient-id", "", "Filter by recipient ID")
	reminderListCmd.Flags().Int("limit", 50, "Max number of reminders to return")
	reminderListCmd.Flags().String("output", "table", "Output format: table or json")

	reminderCreateCmd.Flags().String("title", "", "Reminder title (required)")
	reminderCreateCmd.Flags().String("kind", "", "Reminder kind: system, task, or check_in (required)")
	reminderCreateCmd.Flags().String("body", "", "Reminder body")
	reminderCreateCmd.Flags().String("issue-id", "", "Issue ID for task reminders")
	reminderCreateCmd.Flags().String("remind-at", "", "RFC3339 timestamp for deferred delivery")
	reminderCreateCmd.Flags().String("recipient-type", "", "Recipient type (member, agent)")
	reminderCreateCmd.Flags().String("recipient-id", "", "Recipient ID")
	reminderCreateCmd.Flags().String("output", "json", "Output format: table or json")

	reminderGetCmd.Flags().String("output", "table", "Output format: table or json")
	reminderCancelCmd.Flags().String("output", "json", "Output format: table or json")
}

func runReminderList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	params := url.Values{}
	if v, _ := cmd.Flags().GetString("status"); v != "" {
		params.Set("status", v)
	}
	if v, _ := cmd.Flags().GetString("kind"); v != "" {
		params.Set("kind", v)
	}
	if v, _ := cmd.Flags().GetString("recipient-type"); v != "" {
		params.Set("recipient_type", v)
	}
	if v, _ := cmd.Flags().GetString("recipient-id"); v != "" {
		params.Set("recipient_id", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	path := "/api/reminders"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var resp struct {
		Reminders []map[string]any `json:"reminders"`
		Total     int              `json:"total"`
	}
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return fmt.Errorf("list reminders: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}

	headers := []string{"ID", "KIND", "STATUS", "TITLE", "RECIPIENT", "REMIND_AT"}
	rows := make([][]string, 0, len(resp.Reminders))
	for _, reminder := range resp.Reminders {
		rows = append(rows, []string{
			displayID(strVal(reminder, "id"), false),
			strVal(reminder, "kind"),
			strVal(reminder, "status"),
			strVal(reminder, "title"),
			strVal(reminder, "recipient_type") + ":" + displayID(strVal(reminder, "recipient_id"), false),
			strVal(reminder, "remind_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runReminderCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	title, _ := cmd.Flags().GetString("title")
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	kind, _ := cmd.Flags().GetString("kind")
	if kind == "" {
		return fmt.Errorf("--kind is required (system, task, or check_in)")
	}
	if kind != "system" && kind != "task" && kind != "check_in" {
		return fmt.Errorf("--kind must be system, task, or check_in")
	}

	body := map[string]any{
		"title": title,
		"kind":  kind,
	}
	if v, _ := cmd.Flags().GetString("body"); v != "" {
		body["body"] = v
	}
	if v, _ := cmd.Flags().GetString("issue-id"); v != "" {
		body["issue_id"] = v
	}
	if v, _ := cmd.Flags().GetString("remind-at"); v != "" {
		body["remind_at"] = v
	}
	if v, _ := cmd.Flags().GetString("recipient-type"); v != "" {
		body["recipient_type"] = v
	}
	if v, _ := cmd.Flags().GetString("recipient-id"); v != "" {
		body["recipient_id"] = v
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/reminders", body, &result); err != nil {
		return fmt.Errorf("create reminder: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	printReminderTable([]map[string]any{result})
	return nil
}

func runReminderGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/reminders/"+url.PathEscape(args[0]), &result); err != nil {
		return fmt.Errorf("get reminder: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	printReminderTable([]map[string]any{result})
	return nil
}

func runReminderCancel(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/reminders/"+url.PathEscape(args[0])+"/cancel", nil, &result); err != nil {
		return fmt.Errorf("cancel reminder: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Reminder cancelled: %s (%s)\n", strVal(result, "title"), strVal(result, "id"))
	return nil
}

func printReminderTable(reminders []map[string]any) {
	headers := []string{"ID", "KIND", "STATUS", "TITLE", "RECIPIENT", "REMIND_AT", "DELIVERED_AT"}
	rows := make([][]string, 0, len(reminders))
	for _, reminder := range reminders {
		rows = append(rows, []string{
			strVal(reminder, "id"),
			strVal(reminder, "kind"),
			strVal(reminder, "status"),
			strVal(reminder, "title"),
			strVal(reminder, "recipient_type") + ":" + strVal(reminder, "recipient_id"),
			strVal(reminder, "remind_at"),
			strVal(reminder, "delivered_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}
