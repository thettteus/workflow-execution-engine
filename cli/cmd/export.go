package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
)

// newExportCmd implements `wee export <workflow.yaml>` (REQ-CLI-01, REQ-VERSION-03).
//
// The spec sketches `wee export <name>@<version>`, but M1.8's registry is
// in-memory — there is no persistent on-disk registry to resolve a bare
// name@version against. So the CLI form takes the workflow file: it loads the
// workflow and its sibling Workers into a registry, then exports that
// workflow's own id@version as a portable, hash-stable tar bundle.
func newExportCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "export <workflow.yaml>",
		Short: "Export a workflow and its Workers as a portable bundle",
		Long: "Export bundles a workflow and every Worker it references into one tar of\n" +
			"canonical JSON, importable elsewhere with identical content hashes. Secrets\n" +
			"never travel — definitions carry only ${env:...} references, never values.\n" +
			"Writes to <id>-<version>.tar unless -o is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd, args[0], outPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "", "output file (default <id>-<version>.tar)")
	return cmd
}

func runExport(cmd *cobra.Command, path, outPath string) error {
	wf, err := serialize.LoadWorkflow(path)
	if err != nil {
		return coded(ExitValidation, err)
	}

	reg := registry.New()
	if err := reg.RegisterWorkflow(*wf); err != nil {
		return coded(ExitValidation, err)
	}
	if err := runner.LoadWorkers(path, reg); err != nil {
		return coded(ExitValidation, err)
	}

	archive, err := reg.Export(wf.ID, wf.Version)
	if err != nil {
		return coded(ExitValidation, err)
	}

	if outPath == "" {
		outPath = fmt.Sprintf("%s-%s.tar", wf.ID, wf.Version)
	}
	if err := os.WriteFile(outPath, archive, 0o644); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "exported %s@%s → %s (%d bytes)\n", wf.ID, wf.Version, outPath, len(archive))

	// M2.3: declare the same read-only/write-capable, cost, and tool facts a
	// UI gallery card would show, so a CLI-only user gets the safety
	// declaration too (REQ-UI-16's "declare whether they can write" isn't
	// scoped to the UI).
	facts := registry.DeriveTemplateFacts(*wf)
	writeLabel := "read-only"
	if facts.WriteCapable {
		writeLabel = "write-capable"
	}
	tools := "none"
	if len(facts.Tools) > 0 {
		tools = strings.Join(facts.Tools, ", ")
	}
	fmt.Fprintf(out, "  %s · expected cost ≤ $%.2f · expected duration ≤ %dms · tools: %s\n",
		writeLabel, facts.ExpectedCostUsd, facts.ExpectedDurationMs, tools)
	return nil
}
