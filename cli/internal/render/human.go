package render

import (
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"

	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
)

// Styles for the live human view. lipgloss degrades to plain text when the
// output is not a color terminal (a pipe, a test buffer, CI), so the words
// stay intact and only a TTY sees color — no full-screen TUI (the milestone's
// "keep it simple").
var (
	styleHeader   = lipgloss.NewStyle().Bold(true)
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleFail     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleCacheHit = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")) // distinct badge
)

// humanRenderer prints a readable, incrementally-styled line per meaningful
// event, plus a running total it can print at the end. The node status set is
// the same one the JSON renderer emits — one event stream, two views (PRIN-02).
type humanRenderer struct {
	w    io.Writer
	cost float64
	toks int64
}

// Human returns a Renderer that writes styled status lines to w.
func Human(w io.Writer) Renderer {
	return &humanRenderer{w: w}
}

func (r *humanRenderer) Event(ev domain.Event) {
	switch ev.Type {
	case domain.ExecutionStarted:
		fmt.Fprintln(r.w, styleHeader.Render(fmt.Sprintf("▶ %s@%s", str(ev.Payload["workflow"]), str(ev.Payload["version"]))))
	case domain.WorkerStarted:
		fmt.Fprintf(r.w, "  %s %s\n", styleDim.Render("·"), ev.NodeID)
	case domain.CacheHit:
		badge := styleCacheHit.Render(" CACHE HIT ")
		fmt.Fprintf(r.w, "  %s %s  %s\n", styleOK.Render("⤿"), ev.NodeID, badge+styleDim.Render(fmt.Sprintf(" saved $%.4f", num(ev.Payload["savedCostUsd"]))))
	case domain.ToolCalled:
		fmt.Fprintf(r.w, "  ⚙ %s  %s\n", ev.NodeID, styleDim.Render("tool call"))
	case domain.Retry:
		fmt.Fprintf(r.w, "  %s %s  %s\n", styleWarn.Render("↻"), ev.NodeID, styleDim.Render("retry ("+str(ev.Payload["reason"])+")"))
	case domain.WorkerFinished:
		c, tk := num(ev.Payload["costUsd"]), int64(num(ev.Payload["tokens"]))
		r.cost += c
		r.toks += tk
		fmt.Fprintf(r.w, "  %s %s  %s\n", styleOK.Render("✓"), ev.NodeID, styleDim.Render(fmt.Sprintf("$%.4f  %d tok  (running $%.4f)", c, tk, r.cost)))
	case domain.ContractViolation:
		fmt.Fprintf(r.w, "  %s %s  %s\n", styleWarn.Render("⚠"), ev.NodeID, styleDim.Render("contract violation"))
	case domain.Failure:
		fmt.Fprintf(r.w, "  %s %s  %s\n", styleFail.Render("✗"), ev.NodeID, str(ev.Payload["error"]))
	case domain.BudgetWarning:
		fmt.Fprintln(r.w, styleWarn.Render("  ! budget warning"))
	case domain.BudgetExceeded:
		fmt.Fprintln(r.w, styleFail.Render("  ! budget exceeded"))
	case domain.Cancelled:
		fmt.Fprintln(r.w, styleWarn.Render("  ⏹ cancelled"))
	}
}

func (r *humanRenderer) Finish(res *engine.Result) {
	if res == nil {
		return
	}
	line := fmt.Sprintf("%s — %d node(s), $%.4f, %d tokens", res.State, len(res.Nodes), res.SpentCostUSD, res.SpentTokens)
	style := styleOK
	if res.State != domain.ExecutionSucceeded {
		style = styleFail
	}
	fmt.Fprintf(r.w, "\n%s\n", style.Bold(true).Render(line))
}

// str/num coerce a payload value (decoded from JSON, so numbers are float64)
// without panicking on a missing or unexpected key.
func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}
