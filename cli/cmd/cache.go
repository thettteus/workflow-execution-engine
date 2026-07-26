package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/core/cache"
)

// newCacheCmd implements `wee cache ls|inspect|clear` (REQ-CLI-01, REQ-CACHE-04),
// wrapping core/cache's inspection API over the workspace's cache index.
func newCacheCmd() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and manage the node cache",
		Long:  "List, inspect, or clear the node cache — the record of which node outputs can be reused (REQ-CACHE-04).",
	}
	cmd.PersistentFlags().StringVar(&workspace, "workspace", workspaceDir, "workspace state directory")

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List every cache entry (key, artifact, cost saved)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := cache.New(workspace).List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintln(out, "cache is empty")
				return nil
			}
			for _, e := range entries {
				fmt.Fprintf(out, "%s  %s  $%.4f  %d tok  %s\n", shortHash(e.Key), shortHash(e.ArtifactHash), e.CostUSD, e.Tokens, e.ArtifactType)
			}
			return nil
		},
	}

	inspect := &cobra.Command{
		Use:   "inspect <key>",
		Short: "Show one cache entry's recorded result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, ok, err := cache.New(workspace).Inspect(args[0])
			if err != nil {
				return err
			}
			if !ok {
				return coded(ExitValidation, fmt.Errorf("no cache entry for key %q", args[0]))
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "key:      %s\n", e.Key)
			fmt.Fprintf(out, "artifact: %s (%s)\n", e.ArtifactHash, e.ArtifactType)
			fmt.Fprintf(out, "saved:    $%.4f, %d tokens\n", e.CostUSD, e.Tokens)
			fmt.Fprintf(out, "created:  %s\n", e.CreatedAt)
			return nil
		},
	}

	clear := &cobra.Command{
		Use:   "clear",
		Short: "Remove every cache entry (artifacts in the store are kept)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cache.New(workspace).Clear(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "cache cleared")
			return nil
		},
	}

	cmd.AddCommand(ls, inspect, clear)
	return cmd
}
