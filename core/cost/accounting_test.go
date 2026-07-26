package cost_test

import (
	"math"
	"testing"

	"github.com/thettteus/workflow-execution-engine/core/cost"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeKnownModel(t *testing.T) {
	// gpt-4o: $2.50/1M in, $10.00/1M out. 1000 in + 500 out.
	got := cost.Compute("openai", "gpt-4o", 1000, 500)
	want := 1000*2.50/1e6 + 500*10.00/1e6
	if !approx(got, want) {
		t.Errorf("Compute = %v, want %v", got, want)
	}
}

func TestComputeAnthropic(t *testing.T) {
	got := cost.Compute("anthropic", "claude-3-5-haiku", 2000, 1000)
	want := 2000*0.80/1e6 + 1000*4.00/1e6
	if !approx(got, want) {
		t.Errorf("Compute = %v, want %v", got, want)
	}
}

func TestComputeUnknownIsFree(t *testing.T) {
	// Self-hosted / unlisted model → $0 (REQ-MODEL-04 keeps local models free).
	if got := cost.Compute("openai", "llama3-local", 10_000, 10_000); got != 0 {
		t.Errorf("unknown model should cost $0, got %v", got)
	}
	if got := cost.Compute("somevendor", "x", 100, 100); got != 0 {
		t.Errorf("unknown provider should cost $0, got %v", got)
	}
}

func TestComputeDefaultsToOpenAI(t *testing.T) {
	got := cost.Compute("", "gpt-4o-mini", 1_000_000, 0)
	if !approx(got, 0.15) {
		t.Errorf("empty provider should resolve to openai; got %v want 0.15", got)
	}
}
