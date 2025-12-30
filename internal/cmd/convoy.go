package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Convoy command flags
var (
	convoyMolecule   string
	convoyNotify     string
	convoyStatusJSON bool
	convoyListJSON   bool
	convoyListStatus string
)

var convoyCmd = &cobra.Command{
	Use:     "convoy",
	GroupID: GroupWork,
	Short:   "Manage cross-rig issue tracking",
	Long: `Manage convoys for tracking issues across multiple rigs.

A convoy is a tracking unit that monitors issues across project chains.
Unlike swarms (which coordinate parallel work), convoys simply track
related issues and auto-close when all tracked issues complete.

CONVOY VS SWARM:
  Swarm:  Multiple polecats working on tasks from a shared base commit
  Convoy: Cross-rig issue tracker, monitors progress without spawning workers

TRACKING SEMANTICS:
  - 'tracks' relation is non-blocking (tracked issues don't block convoy)
  - Cross-prefix capable (convoy in hq-* tracks issues in gt-*, bd-*)
  - Reactive completion: convoy auto-closes when all tracked issues close

COMMANDS:
  create    Create a new convoy tracking specified issues
  status    Show convoy progress and tracked issues
  list      List all convoys`,
}

var convoyCreateCmd = &cobra.Command{
	Use:   "create <name> [issues...]",
	Short: "Create a new convoy",
	Long: `Create a new convoy that tracks the specified issues.

The convoy is created in town-level beads (hq-* prefix) and can track
issues across any rig.

Examples:
  gt convoy create "Deploy v2.0" gt-abc bd-xyz
  gt convoy create "Release prep" gt-abc --notify mayor/
  gt convoy create "Feature rollout" gt-a gt-b gt-c --molecule mol-release`,
	Args: cobra.MinimumNArgs(1),
	RunE: runConvoyCreate,
}

var convoyStatusCmd = &cobra.Command{
	Use:   "status [convoy-id]",
	Short: "Show convoy status",
	Long: `Show detailed status for a convoy.

Displays convoy metadata, tracked issues, and completion progress.
Without an ID, shows status of all active convoys.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConvoyStatus,
}

var convoyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List convoys",
	Long: `List all convoys, optionally filtered by status.

Examples:
  gt convoy list
  gt convoy list --status=open
  gt convoy list --json`,
	RunE: runConvoyList,
}

func init() {
	// Create flags
	convoyCreateCmd.Flags().StringVar(&convoyMolecule, "molecule", "", "Associated molecule ID")
	convoyCreateCmd.Flags().StringVar(&convoyNotify, "notify", "", "Address to notify on completion")

	// Status flags
	convoyStatusCmd.Flags().BoolVar(&convoyStatusJSON, "json", false, "Output as JSON")

	// List flags
	convoyListCmd.Flags().BoolVar(&convoyListJSON, "json", false, "Output as JSON")
	convoyListCmd.Flags().StringVar(&convoyListStatus, "status", "", "Filter by status (open, closed)")

	// Add subcommands
	convoyCmd.AddCommand(convoyCreateCmd)
	convoyCmd.AddCommand(convoyStatusCmd)
	convoyCmd.AddCommand(convoyListCmd)

	rootCmd.AddCommand(convoyCmd)
}

// getTownBeadsDir returns the path to town-level beads directory.
func getTownBeadsDir() (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return filepath.Join(townRoot, ".beads"), nil
}

func runConvoyCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	trackedIssues := args[1:]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Create convoy issue in town beads
	description := fmt.Sprintf("Convoy tracking %d issues", len(trackedIssues))
	if convoyNotify != "" {
		description += fmt.Sprintf("\nNotify: %s", convoyNotify)
	}
	if convoyMolecule != "" {
		description += fmt.Sprintf("\nMolecule: %s", convoyMolecule)
	}

	createArgs := []string{
		"create",
		"--type=convoy",
		"--title=" + name,
		"--description=" + description,
		"--json",
	}

	createCmd := exec.Command("bd", createArgs...)
	createCmd.Dir = townBeads
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	createCmd.Stdout = &stdout
	createCmd.Stderr = &stderr

	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("creating convoy: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	// Parse created convoy ID
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		return fmt.Errorf("parsing convoy response: %w", err)
	}

	convoyID := created.ID
	if convoyID == "" {
		return fmt.Errorf("convoy created but no ID returned")
	}

	// Add 'tracks' relations for each tracked issue
	trackedCount := 0
	for _, issueID := range trackedIssues {
		// Use --type=tracks for non-blocking tracking relation
		depArgs := []string{"dep", "add", convoyID, issueID, "--type=tracks"}
		depCmd := exec.Command("bd", depArgs...)
		depCmd.Dir = townBeads

		if err := depCmd.Run(); err != nil {
			style.PrintWarning("couldn't track %s: %v", issueID, err)
		} else {
			trackedCount++
		}
	}

	// Output
	fmt.Printf("%s Created convoy %s\n\n", style.Bold.Render("✓"), convoyID)
	fmt.Printf("  Name:     %s\n", name)
	fmt.Printf("  Tracking: %d issues\n", trackedCount)
	if len(trackedIssues) > 0 {
		fmt.Printf("  Issues:   %s\n", strings.Join(trackedIssues, ", "))
	}
	if convoyNotify != "" {
		fmt.Printf("  Notify:   %s\n", convoyNotify)
	}
	if convoyMolecule != "" {
		fmt.Printf("  Molecule: %s\n", convoyMolecule)
	}

	fmt.Printf("\n  %s\n", style.Dim.Render("Convoy auto-closes when all tracked issues complete"))

	return nil
}

func runConvoyStatus(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// If no ID provided, show all active convoys
	if len(args) == 0 {
		return showAllConvoyStatus(townBeads)
	}

	convoyID := args[0]

	// Get convoy details
	showArgs := []string{"show", convoyID, "--json"}
	showCmd := exec.Command("bd", showArgs...)
	showCmd.Dir = townBeads
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	// Parse convoy data
	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Description string   `json:"description"`
		CreatedAt   string   `json:"created_at"`
		ClosedAt    string   `json:"closed_at,omitempty"`
		DependsOn   []string `json:"depends_on,omitempty"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Get tracked issues by querying SQLite directly
	// (bd dep list doesn't properly show cross-rig external dependencies)
	type trackedIssue struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		Type      string `json:"dependency_type"`
		IssueType string `json:"issue_type"`
	}

	tracked := getTrackedIssues(townBeads, convoyID)

	// Count completed
	completed := 0
	for _, t := range tracked {
		if t.Status == "closed" {
			completed++
		}
	}

	if convoyStatusJSON {
		type jsonStatus struct {
			ID        string             `json:"id"`
			Title     string             `json:"title"`
			Status    string             `json:"status"`
			Tracked   []trackedIssueInfo `json:"tracked"`
			Completed int                `json:"completed"`
			Total     int                `json:"total"`
		}
		out := jsonStatus{
			ID:        convoy.ID,
			Title:     convoy.Title,
			Status:    convoy.Status,
			Tracked:   tracked,
			Completed: completed,
			Total:     len(tracked),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("%s %s\n\n", style.Bold.Render(convoy.ID+":"), convoy.Title)
	fmt.Printf("  Status:    %s\n", formatConvoyStatus(convoy.Status))
	fmt.Printf("  Progress:  %d/%d completed\n", completed, len(tracked))
	fmt.Printf("  Created:   %s\n", convoy.CreatedAt)
	if convoy.ClosedAt != "" {
		fmt.Printf("  Closed:    %s\n", convoy.ClosedAt)
	}

	if len(tracked) > 0 {
		fmt.Printf("\n  %s\n", style.Bold.Render("Tracked Issues:"))
		for _, t := range tracked {
			status := "○"
			if t.Status == "closed" {
				status = "✓"
			}
			issueType := t.IssueType
			if issueType == "" {
				issueType = "task"
			}
			fmt.Printf("    %s %s: %s [%s]\n", status, t.ID, t.Title, issueType)
		}
	}

	return nil
}

func showAllConvoyStatus(townBeads string) error {
	// List all convoy-type issues
	listArgs := []string{"list", "--type=convoy", "--status=open", "--json"}
	listCmd := exec.Command("bd", listArgs...)
	listCmd.Dir = townBeads
	var stdout bytes.Buffer
	listCmd.Stdout = &stdout

	if err := listCmd.Run(); err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return fmt.Errorf("parsing convoy list: %w", err)
	}

	if len(convoys) == 0 {
		fmt.Println("No active convoys.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	if convoyStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(convoys)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Active Convoys"))
	for _, c := range convoys {
		fmt.Printf("  %s: %s\n", c.ID, c.Title)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' for detailed status.\n")

	return nil
}

func runConvoyList(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// List convoy-type issues
	listArgs := []string{"list", "--type=convoy", "--json"}
	if convoyListStatus != "" {
		listArgs = append(listArgs, "--status="+convoyListStatus)
	}

	listCmd := exec.Command("bd", listArgs...)
	listCmd.Dir = townBeads
	var stdout bytes.Buffer
	listCmd.Stdout = &stdout

	if err := listCmd.Run(); err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return fmt.Errorf("parsing convoy list: %w", err)
	}

	if convoyListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(convoys)
	}

	if len(convoys) == 0 {
		fmt.Println("No convoys found.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Convoys"))
	for _, c := range convoys {
		status := formatConvoyStatus(c.Status)
		fmt.Printf("  %s: %s %s\n", c.ID, c.Title, status)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' for detailed view.\n")

	return nil
}

func formatConvoyStatus(status string) string {
	switch status {
	case "open":
		return style.Warning.Render("●")
	case "closed":
		return style.Success.Render("✓")
	case "in_progress":
		return style.Info.Render("→")
	default:
		return status
	}
}

// trackedIssueInfo holds info about an issue being tracked by a convoy.
type trackedIssueInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Type      string `json:"dependency_type"`
	IssueType string `json:"issue_type"`
}

// getTrackedIssues queries SQLite directly to get issues tracked by a convoy.
// This is needed because bd dep list doesn't properly show cross-rig external dependencies.
func getTrackedIssues(townBeads, convoyID string) []trackedIssueInfo {
	dbPath := filepath.Join(townBeads, "beads.db")

	// Query tracked dependencies from SQLite
	queryCmd := exec.Command("sqlite3", "-json", dbPath,
		fmt.Sprintf(`SELECT depends_on_id, type FROM dependencies WHERE issue_id = '%s' AND type = 'tracks'`, convoyID))

	var stdout bytes.Buffer
	queryCmd.Stdout = &stdout
	if err := queryCmd.Run(); err != nil {
		return nil
	}

	var deps []struct {
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &deps); err != nil {
		return nil
	}

	var tracked []trackedIssueInfo
	for _, dep := range deps {
		issueID := dep.DependsOnID

		// Handle external reference format: external:rig:issue-id
		if strings.HasPrefix(issueID, "external:") {
			parts := strings.SplitN(issueID, ":", 3)
			if len(parts) == 3 {
				issueID = parts[2] // Extract the actual issue ID
			}
		}

		// Try to get issue details from the appropriate rig
		info := trackedIssueInfo{
			ID:   issueID,
			Type: dep.Type,
		}

		// Query issue status (try to find it in any known beads location)
		if details := getIssueDetails(issueID); details != nil {
			info.Title = details.Title
			info.Status = details.Status
			info.IssueType = details.IssueType
		} else {
			info.Title = "(external)"
			info.Status = "unknown"
		}

		tracked = append(tracked, info)
	}

	return tracked
}

// issueDetails holds basic issue info.
type issueDetails struct {
	ID        string
	Title     string
	Status    string
	IssueType string
}

// getIssueDetails fetches issue details by trying to show it via bd.
func getIssueDetails(issueID string) *issueDetails {
	// Use bd show with routing - it should find the issue in the right rig
	showCmd := exec.Command("bd", "show", issueID, "--json")
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return nil
	}

	var issues []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		IssueType string `json:"issue_type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil || len(issues) == 0 {
		return nil
	}

	return &issueDetails{
		ID:        issues[0].ID,
		Title:     issues[0].Title,
		Status:    issues[0].Status,
		IssueType: issues[0].IssueType,
	}
}
