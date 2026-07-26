package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/core/serialize"
)

// newListCmd implements `wee list` (REQ-CLI-01): the workflows in the current
// directory and the executions recorded in the workspace.
func newListCmd() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workflows in this directory and recorded executions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			fmt.Fprintln(out, "workflows:")
			for _, wf := range findWorkflows(".") {
				fmt.Fprintf(out, "  %s\n", wf)
			}

			fmt.Fprintln(out, "executions:")
			for _, id := range listExecutions(workspace) {
				fmt.Fprintf(out, "  %s\n", id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", workspaceDir, "workspace state directory")
	return cmd
}

// findWorkflows returns the *.yaml/*.yml files in dir that parse as a workflow
// (an id and at least one node), so worker files and unrelated YAML are skipped.
func findWorkflows(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".yaml" && filepath.Ext(name) != ".yml" {
			continue
		}
		wf, err := serialize.LoadWorkflow(filepath.Join(dir, name))
		if err != nil || wf.ID == "" || len(wf.Nodes) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%s  (%s@%s)", name, wf.ID, wf.Version))
	}
	sort.Strings(out)
	return out
}

// listExecutions returns the recorded execution ids under the workspace.
func listExecutions(workspace string) []string {
	entries, err := os.ReadDir(filepath.Join(workspace, "executions"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
