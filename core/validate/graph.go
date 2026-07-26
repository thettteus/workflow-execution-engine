package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/thettteus/workflow-execution-engine/core/domain"
)

// GraphError is a workflow graph-validation failure carrying one Problem per
// issue found.
type GraphError struct {
	src      LineResolver
	Problems []Problem
}

func (e *GraphError) Error() string {
	return formatProblems("workflow graph is invalid", e.src, e.Problems)
}

// Graph validates a Workflow's node/edge graph: node ids are unique, every edge
// resolves to existing nodes, there are no cycles, no node is an orphan, and
// every artifact a ContextPolicy references is produced by an upstream node.
// src is optional; when provided, problems gain source line numbers.
func Graph(wf *domain.Workflow, src LineResolver) error {
	var problems []Problem

	// Node ids and their pointer locations.
	index := make(map[string]int, len(wf.Nodes))
	for i, n := range wf.Nodes {
		ptr := fmt.Sprintf("/nodes/%d/id", i)
		if n.ID == "" {
			problems = append(problems, resolveLine(src, Problem{Pointer: ptr, Message: "node has an empty id"}))
			continue
		}
		if prev, dup := index[n.ID]; dup {
			problems = append(problems, resolveLine(src, Problem{
				Pointer: ptr,
				Message: fmt.Sprintf("duplicate node id %q (first defined at node index %d)", n.ID, prev),
			}))
			continue
		}
		index[n.ID] = i
	}

	problems = append(problems, checkNodeRef(wf, src)...)
	problems = append(problems, checkInputRefs(wf, src)...)

	// Edge resolution. Only edges whose endpoints both exist feed the graph
	// analysis below; unresolved endpoints are reported here.
	adj := make(map[string][]string, len(index))    // from -> [to]
	indeg := make(map[string]int, len(index))        // node -> incoming count
	touched := make(map[string]bool, len(index))     // node participates in an edge
	for i, e := range wf.Edges {
		fromOK := edgeEndpointOK(index, src, i, "from", e.From, &problems)
		toOK := edgeEndpointOK(index, src, i, "to", e.To, &problems)
		if !fromOK || !toOK {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
		touched[e.From] = true
		touched[e.To] = true
	}

	// Orphans: with more than one node, a node touching no edge is unreachable.
	if len(wf.Nodes) > 1 {
		for _, n := range wf.Nodes {
			if n.ID != "" && !touched[n.ID] {
				problems = append(problems, resolveLine(src, Problem{
					Pointer: fmt.Sprintf("/nodes/%d", index[n.ID]),
					Message: fmt.Sprintf("node %q is an orphan (no edges in or out)", n.ID),
				}))
			}
		}
	}

	// Cycles. If one is found, report it and skip the upstream check (which
	// assumes a DAG).
	if cycle := findCycle(wf.Nodes, adj); cycle != nil {
		problems = append(problems, resolveLine(src, Problem{
			Pointer: fmt.Sprintf("/nodes/%d", index[cycle[0]]),
			Message: "cycle detected: " + strings.Join(cycle, " -> "),
		}))
	} else {
		problems = append(problems, checkContextArtifacts(wf, index, adj, src)...)
	}

	if len(problems) == 0 {
		return nil
	}
	return &GraphError{src: src, Problems: problems}
}

// edgeEndpointOK reports (and returns false) when an edge endpoint names a node
// that does not exist.
func edgeEndpointOK(index map[string]int, src LineResolver, edgeIdx int, side, id string, problems *[]Problem) bool {
	if _, ok := index[id]; ok {
		return true
	}
	*problems = append(*problems, resolveLine(src, Problem{
		Pointer: fmt.Sprintf("/edges/%d/%s", edgeIdx, side),
		Message: fmt.Sprintf("edge %d %s references unknown node %q", edgeIdx, side, id),
	}))
	return false
}

// findCycle returns the node ids forming a cycle (closing back to the first),
// or nil if the graph is acyclic. Node order is honored for determinism.
func findCycle(nodes []domain.Node, adj map[string][]string) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(nodes))
	var stack []string

	var dfs func(id string) []string
	dfs = func(id string) []string {
		color[id] = gray
		stack = append(stack, id)
		for _, next := range adj[id] {
			switch color[next] {
			case white:
				if c := dfs(next); c != nil {
					return c
				}
			case gray:
				// Back edge: extract the cycle from the stack.
				for i, s := range stack {
					if s == next {
						return append(append([]string{}, stack[i:]...), next)
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}

	for _, n := range nodes {
		if n.ID != "" && color[n.ID] == white {
			if c := dfs(n.ID); c != nil {
				return c
			}
		}
	}
	return nil
}

// checkNodeRef verifies every node declares exactly one of Worker or Tool
// (ADR 0008, REQ-WORKER-04) — a Worker-backed node runs an LLM role; a
// Tool-backed node runs a deterministic tool call; a node is never both, and
// never neither.
func checkNodeRef(wf *domain.Workflow, src LineResolver) []Problem {
	var problems []Problem
	for i, n := range wf.Nodes {
		if n.ID == "" {
			continue // already reported by the id-uniqueness pass
		}
		hasWorker := n.Worker != ""
		hasTool := n.Tool != nil
		ptr := fmt.Sprintf("/nodes/%d", i)
		switch {
		case !hasWorker && !hasTool:
			problems = append(problems, resolveLine(src, Problem{
				Pointer: ptr,
				Message: fmt.Sprintf("node %q references neither a worker nor a tool — exactly one is required", n.ID),
			}))
		case hasWorker && hasTool:
			problems = append(problems, resolveLine(src, Problem{
				Pointer: ptr,
				Message: fmt.Sprintf("node %q references both a worker and a tool — exactly one is required", n.ID),
			}))
		}
	}
	return problems
}

// inputPlaceholderRe matches a leaf value that is, in its entirety, an
// "${input:NAME}" placeholder (core/engine/tool_input.go resolves the same
// grammar at run time; this is a static, pre-run check over the same syntax).
var inputPlaceholderRe = regexp.MustCompile(`^\$\{input:(.+)\}$`)

// checkInputRefs verifies every "${input:NAME}" a tool-backed node's input
// tree references names a Workflow.Inputs declaration (REQ-INPUT-01) — the
// same "reference must resolve" discipline checkContextArtifacts applies to
// artifact refs, applied here to declared workflow inputs instead.
func checkInputRefs(wf *domain.Workflow, src LineResolver) []Problem {
	declared := make(map[string]bool, len(wf.Inputs))
	for _, d := range wf.Inputs {
		declared[d.Name] = true
	}
	var problems []Problem
	for i, n := range wf.Nodes {
		if n.Tool == nil {
			continue
		}
		walkInputRefs(n.Tool.Input, fmt.Sprintf("/nodes/%d/tool/input", i), func(name, ptr string) {
			if !declared[name] {
				problems = append(problems, resolveLine(src, Problem{
					Pointer: ptr,
					Message: fmt.Sprintf("node %q references undeclared workflow input %q (add it to the workflow's top-level \"inputs\")", n.ID, name),
				}))
			}
		})
	}
	return problems
}

// walkInputRefs recurses through a tool call's input tree (the same shape
// resolveToolInput walks at run time), reporting every "${input:NAME}"
// placeholder leaf it finds via report.
func walkInputRefs(v any, ptr string, report func(name, ptr string)) {
	switch val := v.(type) {
	case string:
		if m := inputPlaceholderRe.FindStringSubmatch(val); m != nil {
			report(m[1], ptr)
		}
	case map[string]any:
		for k, sub := range val {
			walkInputRefs(sub, ptr+"/"+k, report)
		}
	case []any:
		for i, sub := range val {
			walkInputRefs(sub, fmt.Sprintf("%s/%d", ptr, i), report)
		}
	}
}

// checkContextArtifacts verifies that every node id referenced by an
// "artifacts" ContextPolicy is a strict ancestor (upstream) of the node.
func checkContextArtifacts(wf *domain.Workflow, index map[string]int, adj map[string][]string, src LineResolver) []Problem {
	var problems []Problem
	for i, n := range wf.Nodes {
		if n.ContextPolicy == nil || n.ContextPolicy.Mode != domain.ContextArtifacts || n.ContextPolicy.Params == nil {
			continue
		}
		ancestors := ancestorsOf(n.ID, adj)
		for j, ref := range n.ContextPolicy.Params.Artifacts {
			ptr := fmt.Sprintf("/nodes/%d/contextPolicy/params/artifacts/%d", i, j)
			if _, exists := index[ref]; !exists {
				problems = append(problems, resolveLine(src, Problem{
					Pointer: ptr,
					Message: fmt.Sprintf("node %q references artifact from unknown node %q", n.ID, ref),
				}))
				continue
			}
			if !ancestors[ref] {
				problems = append(problems, resolveLine(src, Problem{
					Pointer: ptr,
					Message: fmt.Sprintf("node %q reads an artifact from %q, which is not upstream of it", n.ID, ref),
				}))
			}
		}
	}
	return problems
}

// ancestorsOf returns the set of nodes that can reach target through adj.
func ancestorsOf(target string, adj map[string][]string) map[string]bool {
	// Build the reverse adjacency once, then flood from target.
	rev := make(map[string][]string)
	for from, tos := range adj {
		for _, to := range tos {
			rev[to] = append(rev[to], from)
		}
	}
	seen := make(map[string]bool)
	var stack []string
	stack = append(stack, rev[target]...)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		stack = append(stack, rev[cur]...)
	}
	return seen
}
