package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/thettteus/workflow-execution-engine/cli/internal/runner"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/model/providers"
	"github.com/thettteus/workflow-execution-engine/core/server"
	"github.com/thettteus/workflow-execution-engine/core/settings"
)

// newServeCmd implements `wee serve` (REQ-CLI-01 command surface, REQ-UI-02) —
// the durable local control plane (M2.2, REQ-CTRL-*). It exposes the workspace's
// event log over HTTP (WebSocket for the live stream, ADR 0010) and drives the
// run lifecycle: start, cancel, resume, retry (optionally from a node),
// re-execute, clear cache, export bundle — plus durable non-secret settings. On
// startup it reconciles any run a prior process left in flight (ADR 0012).
func newServeCmd() *cobra.Command {
	var addr, workspace, dir, cache, templates, uiDir string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local control plane (event stream + run controls) for the UI",
		Long: "Serve exposes the workspace over HTTP for the visual UI:\n" +
			"  GET  /api/executions              list recorded and in-flight runs\n" +
			"  GET  /api/executions/{id}         a run's full recorded events (audit)\n" +
			"  GET  /api/executions/{id}/events  live event stream (WebSocket)\n" +
			"  GET  /api/executions/{id}/progress  derived progress + liveness\n" +
			"  GET  /api/executions/{id}/bundle  download a portable execution bundle (tar)\n" +
			"  POST /api/run                     start a run ({\"workflow\":\"<path under --dir>\",\"inputs\":{...}})\n" +
			"  POST /api/executions/{id}/cancel  cancel an in-flight run\n" +
			"  POST /api/executions/{id}/resume  resume / retry failed nodes\n" +
			"  POST /api/executions/{id}/retry   retry ({\"from\":\"<nodeId>\"} to re-run from a node)\n" +
			"  POST /api/executions/{id}/reexecute  re-run the frozen workflow as a new execution\n" +
			"  POST /api/cache/clear             clear cache (all / keys / a run's node)\n" +
			"  GET|PUT /api/settings             durable, non-secret settings\n" +
			"  GET  /api/templates               list `wee export` bundles under --templates\n\n" +
			"Each event frame is byte-identical to one line of `wee run --json` and is\n" +
			"pushed, not polled (ADR 0010). The UI is a pure client of it. With --ui-dir,\n" +
			"serve also hosts the built React app at / for single-process self-hosting.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cacheMode, err := parseCacheMode(cache)
			if err != nil {
				return coded(ExitValidation, err)
			}
			if uiDir != "" {
				if _, err := os.Stat(filepath.Join(uiDir, "index.html")); err != nil {
					return coded(ExitValidation, fmt.Errorf("--ui-dir %s does not contain index.html: %w", uiDir, err))
				}
			}
			srv := server.New(server.Config{
				Workspace:    workspace,
				Assemble:     runAssembler(dir, workspace),
				NewID:        runner.NewExecutionID,
				DefaultCache: cacheMode,
				Dir:          dir,
				TemplatesDir: templates,
				UIDir:        uiDir,
			})
			// Settle any run a prior process left in flight before accepting
			// requests, so no execution is ever reported as silently running.
			srv.Reconcile()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wee serve listening on http://%s\n", addr)
			fmt.Fprintf(out, "  workspace: %s   workflows under: %s\n", workspace, dir)
			fmt.Fprintf(out, "  stream:    GET http://%s/api/executions/{id}/events\n", addr)
			if templates != "" {
				fmt.Fprintf(out, "  templates: %s\n", templates)
			}
			if uiDir != "" {
				fmt.Fprintf(out, "  ui:        http://%s/ (%s)\n", addr, uiDir)
			}
			return srv.ListenAndServe(addr)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&addr, "addr", "127.0.0.1:7676", "host:port to listen on")
	fl.StringVar(&workspace, "workspace", workspaceDir, "workspace state directory")
	fl.StringVar(&dir, "dir", ".", "base directory that run/control workflow paths resolve against")
	fl.StringVar(&cache, "cache", "on", "default cache mode for started runs: on | off | readonly")
	fl.StringVar(&templates, "templates", "", "directory of `wee export` bundles (*.tar) for the UI's template gallery; empty disables it")
	fl.StringVar(&uiDir, "ui-dir", "", "serve a built UI directory at / for self-hosted operation")
	return cmd
}

// runAssembler builds the server's Assembler: it resolves ref as a workflow file
// under dir and assembles the engine exactly as `wee run` does — the same
// file-resolution, Worker-loading, and tool-wiring path — so a run started
// through the API behaves identically to the CLI. Persisted provider base URLs
// (settings.json, REQ-MODEL-04) are read fresh on each call and applied, so a
// self-hosted endpoint configured in the UI takes effect without a restart. The
// concrete provider packages stay behind providers.Configured (REQ-MODEL-01).
func runAssembler(dir, workspace string) server.Assembler {
	return func(ref string) (*server.Assembly, error) {
		path := filepath.Join(dir, ref)
		set, _ := settings.New(workspace).Load()
		openAICompatible := map[string]string{}
		openAIBaseURL := set.ProviderBaseURLs["openai"]
		anthropicBaseURL := set.ProviderBaseURLs["anthropic"]
		for _, c := range set.Connections {
			if c.Kind != settings.ConnectionKindModelProvider || c.ID == "" {
				continue
			}
			switch c.Type {
			case "openai":
				if c.ID == "openai" && c.BaseURL != "" {
					openAIBaseURL = c.BaseURL
				}
			case "anthropic":
				if c.ID == "anthropic" && c.BaseURL != "" {
					anthropicBaseURL = c.BaseURL
				}
			case "openai-compatible":
				openAICompatible[c.ID] = c.BaseURL
			}
		}
		provReg := providers.Configured(providers.Config{
			OpenAIBaseURL:    openAIBaseURL,
			AnthropicBaseURL: anthropicBaseURL,
			OpenAICompatible: openAICompatible,
		})
		asm, err := runner.LoadWith(path, workspace, provReg)
		if err != nil {
			return nil, err
		}
		if err := validateWorkflowFile(asm.Workflow, path); err != nil {
			return nil, err
		}
		return &server.Assembly{
			Scheduler:        asm.Scheduler,
			Workflow:         asm.Workflow,
			DefinitionHashes: asm.Registry.DefinitionHashes(*asm.Workflow),
			Workers:          asm.Registry.Workers(*asm.Workflow),
			ConnectionRefs:   connectionRefs(set, asm.Registry.Workers(*asm.Workflow)),
		}, nil
	}
}

func connectionRefs(set settings.Settings, workers map[string]domain.Worker) map[string]engine.ConnectionRef {
	byID := map[string]settings.Connection{}
	for _, c := range set.Connections {
		byID[c.ID] = c
	}
	out := map[string]engine.ConnectionRef{}
	for _, w := range workers {
		if c, ok := byID[w.Model.Provider]; ok {
			out[c.ID] = engine.ConnectionRef{
				ID:        c.ID,
				Label:     c.Label,
				Kind:      string(c.Kind),
				Type:      c.Type,
				BaseURL:   c.BaseURL,
				SecretEnv: c.SecretEnv,
				Defaults:  c.Defaults,
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
