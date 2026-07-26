// Package runner assembles a full engine from a workflow file on disk — the
// wiring the CLI's run/replay/inspect commands share. It is the one place the
// CLI turns files into a live Scheduler: it loads the workflow and its Workers,
// registers the model providers and sandboxed tools, and composes the
// model-backed and tool-backed executors behind one Scheduler.
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/model"
	"github.com/thettteus/workflow-execution-engine/core/model/providers"
	"github.com/thettteus/workflow-execution-engine/core/registry"
	"github.com/thettteus/workflow-execution-engine/core/serialize"
	"github.com/thettteus/workflow-execution-engine/core/store"
	"github.com/thettteus/workflow-execution-engine/core/tool"
	"github.com/thettteus/workflow-execution-engine/core/tool/filesystem"
	"github.com/thettteus/workflow-execution-engine/core/tool/git"
	"github.com/thettteus/workflow-execution-engine/core/tool/http"
	"github.com/thettteus/workflow-execution-engine/core/tool/terminal"
)

// Assembly is a wired-up engine ready to run a specific workflow. It also
// exposes the pieces later steps need: the Registry (to pin definition hashes),
// the event Log and artifact Store (for streaming and audit), and the base dir.
type Assembly struct {
	Scheduler *engine.Scheduler
	Workflow  *domain.Workflow
	Registry  *registry.Registry
	Log       *eventlog.Log
	Store     *store.Store
	BaseDir   string
}

// Load builds an Assembly from a workflow file using the default (public)
// provider endpoints. See LoadWith to supply a provider registry configured
// from persisted settings (e.g. a self-hosted base URL, M2.2).
func Load(workflowPath, baseDir string) (*Assembly, error) {
	return LoadWith(workflowPath, baseDir, providers.Default())
}

// LoadWith is Load with an injected provider registry, so the caller controls
// provider configuration (base URLs from settings, REQ-MODEL-04) while keeping
// the concrete openai/anthropic imports out of the CLI (REQ-MODEL-01). baseDir
// is the workspace state directory (executions, artifacts, cache live under it).
// It loads the workflow's sibling Workers (see LoadWorkers) and wires the four
// sandboxed tools from an optional wee.yaml next to the workflow.
func LoadWith(workflowPath, baseDir string, provReg *model.Registry) (*Assembly, error) {
	wf, err := serialize.LoadWorkflow(workflowPath)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(workflowPath)
	reg := registry.New()
	if err := reg.RegisterWorkflow(*wf); err != nil {
		return nil, fmt.Errorf("register workflow: %w", err)
	}
	if err := LoadWorkers(workflowPath, reg); err != nil {
		return nil, err
	}

	cfg, err := loadConfig(dir)
	if err != nil {
		return nil, err
	}
	tools := buildTools(cfg)

	dispatch := engine.NewDispatchExecutor(
		engine.NewWorkerExecutor(reg, provReg),
		engine.NewToolExecutor(tools),
	)

	log := eventlog.New(baseDir)
	st := store.New(baseDir)
	sched := engine.New(dispatch, st, log, cacheFor(baseDir))

	return &Assembly{
		Scheduler: sched,
		Workflow:  wf,
		Registry:  reg,
		Log:       log,
		Store:     st,
		BaseDir:   baseDir,
	}, nil
}

// LoadWorkers registers every *.worker.yaml / *.worker.yml file beside
// workflowPath into reg, keyed by the Worker's own id@version. This is the
// CLI's Worker-resolution convention: a workflow's Workers live in its
// directory (matching examples/pr-review). Shared by Load and the export
// command.
func LoadWorkers(workflowPath string, reg *registry.Registry) error {
	dir := filepath.Dir(workflowPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read workflow dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".worker.yaml") && !strings.HasSuffix(name, ".worker.yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read worker %s: %w", name, err)
		}
		var w domain.Worker
		if err := serialize.UnmarshalYAML(data, &w); err != nil {
			return fmt.Errorf("decode worker %s: %w", name, err)
		}
		if err := reg.RegisterWorker(w); err != nil {
			return fmt.Errorf("register worker %s: %w", name, err)
		}
	}
	return nil
}

// buildTools wires the four built-in tools from cfg. Allowlists default to empty
// (deny-first, PRIN-10): a tool-backed workflow that needs the terminal or HTTP
// must opt in via wee.yaml. filesystem/git operate under the workspace root.
func buildTools(cfg *config) *tool.Registry {
	tools := tool.NewRegistry()
	tools.Register(filesystem.New(cfg.WorkspaceRoot))
	tools.Register(terminal.New(cfg.WorkspaceRoot, cfg.Terminal.Allow, cfg.terminalTimeout(), domain.ArtifactTestResult))
	tools.Register(git.New(cfg.WorkspaceRoot, cfg.terminalTimeout()))
	tools.Register(http.New(cfg.HTTP.Allow, nil))
	return tools
}

// terminalTimeout resolves the terminal/git command timeout, defaulting to 30s.
func (c *config) terminalTimeout() time.Duration {
	if c.Terminal.TimeoutMs > 0 {
		return time.Duration(c.Terminal.TimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}
