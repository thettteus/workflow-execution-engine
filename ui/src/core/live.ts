// The live-execution reducer: fold the `wee serve` event stream (ADR 0009) into
// the state the workspace renders — per-node status, a Gantt-ready timeline,
// running cost, and the artifacts as they land. It is pure and framework-free
// (no React, no EventSource), so the exact same fold drives a live run and a
// replayed one, and the whole thing is unit-tested without a server.
//
// Events are the one source of truth (PRIN-02): this derives state from them,
// never the other way round. The mapping mirrors what core/engine emits per
// node — WorkerStarted → CacheHit|CacheMiss → [ToolCalled/ToolResult,
// Retry/ContractViolation] → ArtifactCreated → WorkerFinished, or a terminal
// Failure with no WorkerFinished (core/engine/node.go).

export type WFEventType =
  | 'ExecutionStarted'
  | 'ExecutionFinished'
  | 'WorkerStarted'
  | 'WorkerFinished'
  | 'ToolCalled'
  | 'ToolResult'
  | 'ArtifactCreated'
  | 'ContractValidated'
  | 'ContractViolation'
  | 'Retry'
  | 'Failure'
  | 'CacheHit'
  | 'CacheMiss'
  | 'BudgetWarning'
  | 'BudgetExceeded'
  | 'ApprovalRequested'
  | 'ApprovalGranted'
  | 'ApprovalRejected'
  | 'Cancelled'

/** WFEvent mirrors core/domain.Event by JSON tag — the exact object each
 *  WebSocket text frame carries (byte-identical to `wee run --json`). */
export interface WFEvent {
  type: WFEventType
  timestamp: string
  executionId: string
  nodeId?: string
  prevHash: string
  payload?: Record<string, unknown>
}

// A node's live status. The engine emits no distinct "queued" or "skipped"
// event in Phase 1: a node is `pending` until its WorkerStarted arrives, and a
// node skipped by a false conditional edge simply never leaves `pending`.
export type NodeStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cached' | 'paused'

export type RunState = 'idle' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'paused'

export interface NodeLive {
  id: string
  status: NodeStatus
  startedAt?: number // epoch ms, from WorkerStarted
  endedAt?: number // epoch ms, from WorkerFinished / Failure
  costUsd: number
  tokens: number
  cached: boolean
  retries: number
  error?: string
}

export interface LiveArtifact {
  nodeId: string
  hash: string
  type: string
  at: number
}

export interface LiveState {
  executionId: string | null
  workflow?: string
  version?: string
  state: RunState
  startedAt?: number
  endedAt?: number
  nodes: Record<string, NodeLive>
  artifacts: LiveArtifact[]
  totalCostUsd: number
  totalTokens: number
  savedCostUsd: number // avoided spend attributed to cache hits (REQ-METRIC-02 proxy)
  savedTokens: number
  events: WFEvent[]
}

function ms(ts: string): number {
  const t = Date.parse(ts)
  return Number.isNaN(t) ? 0 : t
}

function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function freshNode(id: string): NodeLive {
  return { id, status: 'pending', costUsd: 0, tokens: 0, cached: false, retries: 0 }
}

/** emptyLive is the pre-run state. Seed it with the workflow's node ids so the
 *  canvas shows every node as `pending` before the first event arrives. */
export function emptyLive(nodeIds: string[] = []): LiveState {
  const nodes: Record<string, NodeLive> = {}
  for (const id of nodeIds) nodes[id] = freshNode(id)
  return {
    executionId: null,
    state: 'idle',
    nodes,
    artifacts: [],
    totalCostUsd: 0,
    totalTokens: 0,
    savedCostUsd: 0,
    savedTokens: 0,
    events: [],
  }
}

/** reduce folds one event into the state, immutably (a new state, new nodes
 *  map, new node object) so it drops straight into a store's setState. */
export function reduce(prev: LiveState, ev: WFEvent): LiveState {
  const next: LiveState = {
    ...prev,
    nodes: prev.nodes,
    artifacts: prev.artifacts,
    events: [...prev.events, ev],
  }
  const at = ms(ev.timestamp)
  const p = ev.payload ?? {}

  const patchNode = (id: string, patch: Partial<NodeLive>) => {
    const cur = next.nodes[id] ?? freshNode(id)
    next.nodes = { ...next.nodes, [id]: { ...cur, ...patch } }
  }

  switch (ev.type) {
    case 'ExecutionStarted':
      next.executionId = ev.executionId
      next.state = 'running'
      next.startedAt = at
      next.workflow = str(p.workflow)
      next.version = str(p.version)
      return next

    case 'WorkerStarted':
      if (ev.nodeId) {
        const cur = next.nodes[ev.nodeId] ?? freshNode(ev.nodeId)
        patchNode(ev.nodeId, { status: 'running', startedAt: cur.startedAt ?? at })
      }
      return next

    case 'CacheHit':
      if (ev.nodeId) patchNode(ev.nodeId, { cached: true })
      next.savedCostUsd += num(p.savedCostUsd)
      next.savedTokens += num(p.savedTokens)
      return next

    case 'Retry':
      if (ev.nodeId) {
        const cur = next.nodes[ev.nodeId] ?? freshNode(ev.nodeId)
        patchNode(ev.nodeId, { retries: cur.retries + 1 })
      }
      return next

    case 'ArtifactCreated':
      if (ev.nodeId) {
        next.artifacts = [...next.artifacts, { nodeId: ev.nodeId, hash: str(p.hash), type: str(p.type), at }]
      }
      return next

    case 'WorkerFinished':
      if (ev.nodeId) {
        const cur = next.nodes[ev.nodeId] ?? freshNode(ev.nodeId)
        const cost = num(p.costUsd)
        const tokens = num(p.tokens)
        patchNode(ev.nodeId, {
          status: cur.cached ? 'cached' : 'succeeded',
          endedAt: at,
          costUsd: cost,
          tokens,
        })
        next.totalCostUsd += cost
        next.totalTokens += tokens
      }
      return next

    case 'Failure':
      if (ev.nodeId) {
        // Attempts the contract rejected were still billed; the engine reports
        // that spend on Failure (REQ-BUDGET-03). Absent on older logs → 0.
        const cur = next.nodes[ev.nodeId] ?? freshNode(ev.nodeId)
        const cost = num(p.costUsd)
        const tokens = num(p.tokens)
        patchNode(ev.nodeId, {
          status: 'failed',
          endedAt: at,
          error: str(p.error),
          costUsd: cur.costUsd + cost,
          tokens: cur.tokens + tokens,
        })
        next.totalCostUsd += cost
        next.totalTokens += tokens
      }
      return next

    case 'ApprovalRequested':
      if (ev.nodeId) patchNode(ev.nodeId, { status: 'paused' })
      return next

    case 'Cancelled':
      next.state = 'cancelled'
      return next

    case 'ExecutionFinished': {
      const s = str(p.state)
      next.state =
        s === 'succeeded'
          ? 'succeeded'
          : s === 'cancelled'
            ? 'cancelled'
            : s === 'failed'
              ? 'failed'
              : s === 'paused'
                ? 'paused'
                : next.state
      next.endedAt = at
      return next
    }

    default:
      // CacheMiss, ToolCalled, ToolResult, ContractValidated, ContractViolation,
      // BudgetWarning, BudgetExceeded — recorded in events[], no state change.
      return next
  }
}

/** reduceAll folds a whole event sequence from empty — used to load a finished
 *  run (GET /api/executions/{id}) through the identical path as the live one. */
export function reduceAll(events: WFEvent[], nodeIds: string[] = []): LiveState {
  return events.reduce(reduce, emptyLive(nodeIds))
}

/** A node's Gantt bar as fractions [0,1] of the run's span, for the timeline.
 *  A still-running node extends to `now`. Returns null before the run starts. */
export interface Bar {
  id: string
  left: number
  width: number
  status: NodeStatus
}

export function bars(live: LiveState, now: number): Bar[] {
  const start = live.startedAt
  if (start == null) return []
  const end = live.endedAt ?? now
  const span = Math.max(end - start, 1)
  const out: Bar[] = []
  for (const n of Object.values(live.nodes)) {
    if (n.startedAt == null) continue // never ran (pending/skipped) — no bar
    const a = n.startedAt
    const b = n.endedAt ?? now
    out.push({
      id: n.id,
      left: (a - start) / span,
      width: Math.max((b - a) / span, 0),
      status: n.status,
    })
  }
  return out.sort((x, y) => x.left - y.left)
}
