package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/cli/internal/render"
	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/replay"
)

const cliSmokeWorkflow = `id: cli-smoke
version: 1.0.0
nodes:
  - id: check
    tool:
      toolName: terminal
      input:
        command: echo
        args: ["cli-ok"]
edges: []
budget:
  maxCostUsd: 0
  maxTokens: 0
  maxDurationMs: 30000
  maxRetriesPerNode: 1
`

const cliSmokeConfig = `terminal:
  allow: ["echo"]
  timeoutMs: 5000
`

var (
	cliPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)
	cliTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)
	cliAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	cliDimStyle    = lipgloss.NewStyle().Faint(true)
)

// newCLICmd is the one-command local CLI experience: no files to author, no API
// key, no UI. It proves the binary can validate, run, record, inspect, and
// replay a workflow through the same engine path the normal commands use.
func newCLICmd() *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "cli",
		Short: "Run the zero-config CLI experience",
		Long: "Run a zero-config terminal workflow with polished CLI output. It creates a\n" +
			"temporary workspace, runs one sandboxed tool node, records the execution,\n" +
			"prints the artifact, and shows the exact follow-up commands.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCLI(cmd, keep)
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "keep the generated temporary workflow/workspace so follow-up commands can be run")
	return cmd
}

func runCLI(cmd *cobra.Command, keep bool) error {
	out := cmd.OutOrStdout()
	base, err := os.MkdirTemp("", "wee-cli-*")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(base)
	}
	workflowPath := filepath.Join(base, "check.yaml")
	workspace := filepath.Join(base, ".workflow")
	if err := os.WriteFile(workflowPath, []byte(cliSmokeWorkflow), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, "wee.yaml"), []byte(cliSmokeConfig), 0o644); err != nil {
		return err
	}

	fmt.Fprintln(out, cliPanelStyle.Render(
		cliTitleStyle.Render(" wee cli ")+"\n\n"+
			"Zero-config CLI smoke run\n"+
			cliDimStyle.Render("tool-only, no provider key, recorded under a temporary workspace"),
	))
	fmt.Fprintf(out, "%s workflow  %s\n", cliAccentStyle.Render("•"), workflowPath)
	fmt.Fprintf(out, "%s workspace %s\n\n", cliAccentStyle.Render("•"), workspace)

	asm, err := runner.Load(workflowPath, workspace)
	if err != nil {
		return coded(ExitValidation, err)
	}
	if err := validateWorkflowFile(asm.Workflow, workflowPath); err != nil {
		return coded(ExitValidation, err)
	}

	execID := runner.NewExecutionID(asm.Workflow.ID)
	opts := engine.RunOptions{
		ExecutionID:              execID,
		Budget:                   asm.Workflow.Budget,
		Cache:                    engine.CacheOff,
		DefinitionHashes:         asm.Registry.DefinitionHashes(*asm.Workflow),
		Workers:                  asm.Registry.Workers(*asm.Workflow),
		AllowUnattendedMutations: true,
	}

	res, err := runWithHumanStream(cmd.Context(), out, asm, execID, opts)
	if err != nil {
		return exitForRun(res, err)
	}

	if err := printCLIArtifact(out, asm, execID); err != nil {
		return err
	}
	fmt.Fprintln(out)
	if keep {
		fmt.Fprintln(out, cliPanelStyle.Render(
			cliAccentStyle.Render("Next")+"\n\n"+
				fmt.Sprintf("wee inspect %s --workspace %s\n", execID, workspace)+
				fmt.Sprintf("wee replay %s --workspace %s", execID, workspace),
		))
	} else {
		fmt.Fprintln(out, cliDimStyle.Render("Temporary workspace removed. Re-run with --keep to inspect/replay afterwards."))
	}
	return nil
}

func runWithHumanStream(ctx context.Context, out io.Writer, asm *runner.Assembly, execID string, opts engine.RunOptions) (*engine.Result, error) {
	type outcome struct {
		res *engine.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := asm.Scheduler.Run(ctx, asm.Workflow, opts)
		done <- outcome{res: res, err: err}
	}()

	streamAndFinish := streamer(asm, execID, render.Human(out))
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case o := <-done:
			streamAndFinish(o.res, true)
			return o.res, o.err
		case <-ticker.C:
			streamAndFinish(nil, false)
		}
	}
}

func printCLIArtifact(out io.Writer, asm *runner.Assembly, execID string) error {
	tl, err := replay.NewAuditor(asm.Log, asm.Store).Audit(execID)
	if err != nil {
		return err
	}
	rec, ok := tl.Nodes["check"]
	if !ok {
		return fmt.Errorf("cli smoke output missing node check")
	}
	stdout := terminalStdout(rec.Content)
	if stdout == "" {
		stdout = string(rec.Content)
	}
	stdout = strings.TrimRight(stdout, "\n")
	fmt.Fprintln(out, cliPanelStyle.Render(
		cliAccentStyle.Render("Artifact")+"\n\n"+
			fmt.Sprintf("stdout: %s", stdout),
	))
	return nil
}

func terminalStdout(data []byte) string {
	var payload struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return payload.Stdout
}
