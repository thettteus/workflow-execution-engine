package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/thettteus/workflow-execution-engine/core/cache"
	"github.com/thettteus/workflow-execution-engine/core/diagnostic"
	"github.com/thettteus/workflow-execution-engine/core/domain"
	"github.com/thettteus/workflow-execution-engine/core/engine"
	"github.com/thettteus/workflow-execution-engine/core/eventlog"
	"github.com/thettteus/workflow-execution-engine/core/store"
)

// stub is a configurable NodeExecutor for exercising the scheduler without a
// real model. All fields are read once per call under the lock, so it is safe
// for the concurrent workers the scheduler runs.
type stub struct {
	mu               sync.Mutex
	delay            time.Duration
	starts           map[string]int
	inputs           map[string]int
	startAt          map[string]time.Time
	endAt            map[string]time.Time
	fails            map[string]int  // transient failures remaining per node
	fatal            map[string]bool // node returns a fatal (non-retryable) error
	cost             map[string]float64
	blockUntilCancel map[string]bool // node blocks until ctx is cancelled
	onStart          func(id string) // called (outside the lock) as each node starts
	lastWfInputs     map[string]string
}

func newStub() *stub {
	return &stub{
		starts: map[string]int{}, inputs: map[string]int{},
		startAt: map[string]time.Time{}, endAt: map[string]time.Time{},
		fails: map[string]int{}, fatal: map[string]bool{},
		cost: map[string]float64{}, blockUntilCancel: map[string]bool{},
	}
}

func (s *stub) Execute(ctx context.Context, req engine.NodeRequest) (engine.NodeResult, error) {
	node, inputs := req.Node, req.Inputs
	s.mu.Lock()
	s.starts[node.ID]++
	s.inputs[node.ID] = len(inputs)
	s.lastWfInputs = req.WorkflowInputs
	if s.startAt[node.ID].IsZero() {
		s.startAt[node.ID] = time.Now()
	}
	failsRemaining := s.fails[node.ID]
	if failsRemaining > 0 {
		s.fails[node.ID]--
	}
	fatal := s.fatal[node.ID]
	block := s.blockUntilCancel[node.ID]
	cost := s.cost[node.ID]
	delay := s.delay
	onStart := s.onStart
	s.mu.Unlock()

	if onStart != nil {
		onStart(node.ID)
	}
	if block {
		<-ctx.Done()
		return engine.NodeResult{}, ctx.Err()
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return engine.NodeResult{}, ctx.Err()
		}
	}
	if failsRemaining > 0 {
		err := fmt.Errorf("flaky %s", node.ID)
		err = diagnostic.Wrap(err, diagnostic.KindProvider, "provider_transient", node.ID, "provider.complete", "provider returned a retryable failure", "wait for the retry window or lower concurrency/rate")
		return engine.NodeResult{}, engine.Transient(err)
	}
	if fatal {
		return engine.NodeResult{}, engine.Fatal(fmt.Errorf("boom %s", node.ID))
	}
	s.mu.Lock()
	s.endAt[node.ID] = time.Now()
	s.mu.Unlock()
	content := []byte(fmt.Sprintf(`{"node":%q,"inputs":%d}`, node.ID, len(inputs)))
	return engine.NodeResult{Content: content, Type: domain.ArtifactJSON, CostUSD: cost, Tokens: 10}, nil
}

func (s *stub) startCount(id string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.starts[id] }
func (s *stub) wfInputsSeen() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWfInputs
}
func (s *stub) inputCount(id string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.inputs[id] }
func (s *stub) window(id string) (time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startAt[id], s.endAt[id]
}

// --- test helpers ---

func newScheduler(t *testing.T, exec engine.NodeExecutor) (*engine.Scheduler, *eventlog.Log) {
	t.Helper()
	base := t.TempDir()
	log := eventlog.New(base)
	return engine.New(exec, store.New(base), log, cache.New(base)), log
}

func eventCount(t *testing.T, log *eventlog.Log, execID string, typ domain.EventType) int {
	t.Helper()
	events, err := log.ReadAll(execID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func node(id string) domain.Node { return domain.Node{ID: id, Worker: "w@1.0.0"} }

func waitFor(cond func() bool) bool {
	for i := 0; i < 400; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestDiamondParallelism is M1.3 acceptance #1: in A → {B,C} → D, B and C run
// concurrently and D receives both artifacts.
func TestDiamondParallelism(t *testing.T) {
	stub := newStub()
	stub.delay = 60 * time.Millisecond
	s, _ := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "diamond", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B"), node("C"), node("D")},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "A", To: "C"}, {From: "B", To: "D"}, {From: "C", To: "D"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	if got := stub.inputCount("D"); got != 2 {
		t.Errorf("D received %d inputs, want 2 (both B and C)", got)
	}
	sB, eB := stub.window("B")
	sC, eC := stub.window("C")
	if !(sB.Before(eC) && sC.Before(eB)) {
		t.Errorf("B and C did not overlap: B[%v..%v] C[%v..%v]", sB, eB, sC, eC)
	}
}

// TestResumeSkipsFinishedNodes is M1.3 acceptance #2: cancel mid-execution, then
// Resume — finished nodes are not re-executed and the run completes.
func TestResumeSkipsFinishedNodes(t *testing.T) {
	base := t.TempDir()
	log := eventlog.New(base)
	st := store.New(base)

	wf := &domain.Workflow{
		ID: "line", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B"), node("C"), node("D")},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "B", To: "C"}, {From: "C", To: "D"}},
	}

	// Run 1: concurrency 1 forces A→B→C order; when C starts we cancel, so A and
	// B are finished on disk and C/D are not.
	ctx, cancel := context.WithCancel(context.Background())
	stub1 := newStub()
	stub1.blockUntilCancel["C"] = true
	stub1.onStart = func(id string) {
		if id == "C" {
			cancel()
		}
	}
	res1, _ := engine.New(stub1, st, log, cache.New(base)).Run(ctx, wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if res1.State != domain.ExecutionCancelled {
		t.Fatalf("run 1 state = %s, want cancelled", res1.State)
	}
	if res1.Nodes["A"].State != engine.StateSucceeded || res1.Nodes["B"].State != engine.StateSucceeded {
		t.Fatalf("A and B should be finished before cancel: A=%s B=%s", res1.Nodes["A"].State, res1.Nodes["B"].State)
	}

	// Run 2: resume. A, B are skipped (reused from disk); C, D run.
	stub2 := newStub()
	res2, err := engine.New(stub2, st, log, cache.New(base)).Resume(context.Background(), "e1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res2.State != domain.ExecutionSucceeded {
		t.Fatalf("resume state = %s, want succeeded", res2.State)
	}
	if stub2.startCount("A") != 0 || stub2.startCount("B") != 0 {
		t.Errorf("finished nodes were re-executed on resume: A=%d B=%d", stub2.startCount("A"), stub2.startCount("B"))
	}
	if stub2.startCount("C") == 0 || stub2.startCount("D") == 0 {
		t.Errorf("C and D should run on resume: C=%d D=%d", stub2.startCount("C"), stub2.startCount("D"))
	}
	// No duplicate WorkerStarted for the finished nodes across the whole log.
	events, _ := log.ReadAll("e1")
	ws := map[string]int{}
	for _, ev := range events {
		if ev.Type == domain.WorkerStarted {
			ws[ev.NodeID]++
		}
	}
	if ws["A"] != 1 || ws["B"] != 1 {
		t.Errorf("finished nodes have duplicate WorkerStarted: A=%d B=%d", ws["A"], ws["B"])
	}
}

// TestBudgetExceededHalts is M1.3 acceptance #3: a $0.01 budget halts
// deterministically, emits BudgetExceeded, and returns a distinct error.
func TestBudgetExceededHalts(t *testing.T) {
	stub := newStub()
	stub.cost["A"] = 0.01
	stub.cost["B"] = 0.01
	s, log := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "line", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B")},
		Edges: []domain.Edge{{From: "A", To: "B"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "e1", Concurrency: 1,
		Budget: domain.Budget{MaxCostUSD: 0.01},
	})
	if !errors.Is(err, engine.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if !res.BudgetExceeded || res.State != domain.ExecutionFailed {
		t.Errorf("res = %+v, want BudgetExceeded && failed", res)
	}
	if stub.startCount("B") != 0 {
		t.Errorf("B should not have been dispatched after the budget was exhausted")
	}
	if eventCount(t, log, "e1", domain.BudgetExceeded) != 1 {
		t.Errorf("want exactly one BudgetExceeded event")
	}
}

func TestConditionalEdgeSkipsAndRuns(t *testing.T) {
	run := func(t *testing.T, cond *domain.Condition) *engine.Result {
		stub := newStub()
		s, _ := newScheduler(t, stub)
		wf := &domain.Workflow{
			ID: "cond", Version: "1.0.0",
			Nodes: []domain.Node{node("A"), node("B")},
			Edges: []domain.Edge{{From: "A", To: "B", Condition: cond}},
		}
		res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 2})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res
	}

	// A's output is {"node":"A",...}; a false predicate skips B.
	t.Run("false skips downstream", func(t *testing.T) {
		res := run(t, &domain.Condition{Path: "node", Op: domain.OpEq, Value: "Z"})
		if res.Nodes["B"].State != engine.StateSkipped {
			t.Errorf("B state = %s, want skipped", res.Nodes["B"].State)
		}
		if res.State != domain.ExecutionSucceeded {
			t.Errorf("execution state = %s, want succeeded", res.State)
		}
	})
	t.Run("true runs downstream", func(t *testing.T) {
		res := run(t, &domain.Condition{Path: "node", Op: domain.OpEq, Value: "A"})
		if res.Nodes["B"].State != engine.StateSucceeded {
			t.Errorf("B state = %s, want succeeded", res.Nodes["B"].State)
		}
	})
}

func TestRetryOnTransientError(t *testing.T) {
	stub := newStub()
	stub.fails["A"] = 2 // fail twice, succeed on the third attempt
	s, log := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "retry", Version: "1.0.0",
		Nodes: []domain.Node{node("A")},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{
		ExecutionID: "e1", Concurrency: 1,
		Budget:       domain.Budget{MaxRetriesPerNode: 3},
		RetryBackoff: 0,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Errorf("state = %s, want succeeded", res.State)
	}
	if got := stub.startCount("A"); got != 3 {
		t.Errorf("A ran %d times, want 3 (1 + 2 retries)", got)
	}
	if got := eventCount(t, log, "e1", domain.Retry); got != 2 {
		t.Errorf("Retry events = %d, want 2", got)
	}
	events, err := log.ReadAll("e1")
	if err != nil {
		t.Fatal(err)
	}
	var sawDiagnostic bool
	for _, ev := range events {
		if ev.Type != domain.Retry {
			continue
		}
		diag, ok := ev.Payload["diagnostic"].(map[string]any)
		if ok && diag["nodeId"] == "A" && diag["code"] == "provider_transient" && diag["likelyFix"] != "" {
			sawDiagnostic = true
		}
	}
	if !sawDiagnostic {
		t.Fatalf("Retry events did not include structured node diagnostics: %#v", events)
	}
}

func TestArtifactLimitFailureNamesNodeAndLikelyFix(t *testing.T) {
	stub := newStub()
	base := t.TempDir()
	log := eventlog.New(base)
	s := engine.New(stub, store.NewWithOptions(base, store.WithMaxArtifactBytes(8), store.WithMaxTotalBytes(0)), log, cache.New(base))

	wf := &domain.Workflow{ID: "limit", Version: "1.0.0", Nodes: []domain.Node{node("oversized")}}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err == nil {
		t.Fatal("Run succeeded, want artifact limit failure")
	}
	if res.Nodes["oversized"].State != engine.StateFailed {
		t.Fatalf("node state = %s, want failed", res.Nodes["oversized"].State)
	}
	events, err := log.ReadAll("e1")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != domain.Failure || ev.NodeID != "oversized" {
			continue
		}
		diag, ok := ev.Payload["diagnostic"].(map[string]any)
		if !ok {
			t.Fatalf("Failure missing diagnostic payload: %#v", ev.Payload)
		}
		if diag["kind"] != "artifact" || diag["nodeId"] != "oversized" || diag["likelyFix"] == "" {
			t.Fatalf("diagnostic = %#v", diag)
		}
		return
	}
	t.Fatalf("no Failure event for oversized node: %#v", events)
}

func TestFailurePolicyContinue(t *testing.T) {
	stub := newStub()
	stub.fatal["B"] = true
	s, _ := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "continue", Version: "1.0.0",
		Nodes: []domain.Node{
			node("A"),
			{ID: "B", Worker: "w@1", OnFailure: &domain.FailurePolicy{Mode: domain.FailContinue}},
			node("C"),
		},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "A", To: "C"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 2})
	if err != nil {
		t.Fatalf("continue policy should not return an error, got %v", err)
	}
	if res.Nodes["C"].State != engine.StateSucceeded {
		t.Errorf("C (independent branch) should have run, got %s", res.Nodes["C"].State)
	}
	if res.Nodes["B"].State != engine.StateFailed {
		t.Errorf("B should be failed, got %s", res.Nodes["B"].State)
	}
	if res.State != domain.ExecutionFailed {
		t.Errorf("execution state = %s, want failed (a node failed under continue)", res.State)
	}
}

func TestFailurePolicyFallback(t *testing.T) {
	stub := newStub()
	stub.fatal["B"] = true
	s, _ := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "fallback", Version: "1.0.0",
		Nodes: []domain.Node{
			node("A"),
			{ID: "B", Worker: "w@1", OnFailure: &domain.FailurePolicy{Mode: domain.FailFallback, FallbackNode: "F"}},
			node("C"),
			node("F"),
		},
		Edges: []domain.Edge{{From: "A", To: "B"}, {From: "B", To: "C"}},
	}
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stub.startCount("F") != 1 {
		t.Errorf("fallback node F should have run once, got %d", stub.startCount("F"))
	}
	if res.Nodes["B"].State != engine.StateSucceeded {
		t.Errorf("B should succeed via its fallback, got %s", res.Nodes["B"].State)
	}
	if res.Nodes["C"].State != engine.StateSucceeded {
		t.Errorf("C should run after the fallback resolved B, got %s", res.Nodes["C"].State)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Errorf("execution state = %s, want succeeded", res.State)
	}
}

// TestCancellationNoGoroutineLeak cancels a run with a node in flight and
// verifies clean shutdown: a Cancelled event, cancelled state, and goroutines
// returning to baseline (Run joins every goroutine it spawns before returning).
func TestCancellationNoGoroutineLeak(t *testing.T) {
	stub := newStub()
	stub.blockUntilCancel["A"] = true // root blocks, holding the execution open
	s, log := newScheduler(t, stub)

	wf := &domain.Workflow{
		ID: "cancel", Version: "1.0.0",
		Nodes: []domain.Node{node("A"), node("B")},
		Edges: []domain.Edge{{From: "A", To: "B"}},
	}
	ctx, cancel := context.WithCancel(context.Background())

	before := runtime.NumGoroutine()
	done := make(chan struct{})
	var res *engine.Result
	var err error
	go func() {
		res, err = s.Run(ctx, wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 4})
		close(done)
	}()

	if !waitFor(func() bool { return stub.startCount("A") > 0 }) {
		t.Fatal("node A never started")
	}
	cancel()
	<-done

	if res.State != domain.ExecutionCancelled {
		t.Errorf("state = %s, want cancelled", res.State)
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, engine.ErrCancelled) {
		t.Errorf("err = %v, want a cancellation error", err)
	}
	if eventCount(t, log, "e1", domain.Cancelled) != 1 {
		t.Errorf("want exactly one Cancelled event")
	}
	// Run joined its workers before returning, so goroutines should settle back.
	settled := false
	for i := 0; i < 40; i++ {
		if runtime.NumGoroutine() <= before {
			settled = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !settled {
		t.Errorf("goroutines did not settle: before=%d now=%d", before, runtime.NumGoroutine())
	}
}

func TestLongGraphStressBoundedEventsAndArtifacts(t *testing.T) {
	const n = 300
	stub := newStub()
	base := t.TempDir()
	log := eventlog.New(base)
	s := engine.New(stub, store.New(base), log, cache.New(base))

	wf := &domain.Workflow{ID: "stress", Version: "1.0.0", Nodes: make([]domain.Node, 0, n), Edges: make([]domain.Edge, 0, n-1)}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n%03d", i)
		wf.Nodes = append(wf.Nodes, node(id))
		if i > 0 {
			wf.Edges = append(wf.Edges, domain.Edge{From: fmt.Sprintf("n%03d", i-1), To: id})
		}
	}

	before := runtime.NumGoroutine()
	res, err := s.Run(context.Background(), wf, engine.RunOptions{ExecutionID: "e1", Concurrency: 8})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != domain.ExecutionSucceeded {
		t.Fatalf("state = %s, want succeeded", res.State)
	}
	events, err := log.ReadAll("e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) > n*4+4 {
		t.Fatalf("event count = %d, want bounded by node lifecycle", len(events))
	}
	entries, err := os.ReadDir(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("artifact files = %d, want one per unique node output", len(entries))
	}
	for i := 0; i < 40; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle after stress run: before=%d now=%d", before, runtime.NumGoroutine())
}
