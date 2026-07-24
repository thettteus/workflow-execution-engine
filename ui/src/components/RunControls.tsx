import { useEffect, useState } from 'react'

import { bundleUrl } from '../liveClient'
import { useLive } from '../liveStore'

// RunControls is the durable control plane made visible (M2.2, REQ-CTRL-03): it
// acts on the execution the Run monitor is currently showing (live or loaded
// from History). While a run is in flight it offers Cancel and a live progress
// + liveness readout; once terminal it offers Resume (continue the same
// execution, reusing finished nodes), Re-run (a fresh execution reusing cache),
// Clear cache, and Export bundle. Every action goes through wee serve — nothing
// here re-derives engine state (PRIN-02).
export function RunControls() {
  const connected = useLive((s) => s.connected)
  const live = useLive((s) => s.live)
  const audit = useLive((s) => s.audit)
  const serverUrl = useLive((s) => s.serverUrl)
  const lastEventAt = useLive((s) => s.lastEventAt)
  const cancel = useLive((s) => s.cancel)
  const approve = useLive((s) => s.approve)
  const reject = useLive((s) => s.reject)
  const retry = useLive((s) => s.retry)
  const reexecute = useLive((s) => s.reexecute)
  const clearNodeCache = useLive((s) => s.clearNodeCache)

  const execId = live.executionId || audit?.executionId || null

  // Tick while running so the "idle Ns" liveness readout advances between
  // events; no timer once the run settles (a live tool, not a battery drain).
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!connected) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [connected])

  if (!execId) return null

  const allNodes = Object.values(live.nodes)
  const total = allNodes.length
  const completed = allNodes.filter(
    (n) => n.status === 'succeeded' || n.status === 'cached' || n.status === 'failed',
  ).length
  const idleMs = connected && lastEventAt ? now - lastEventAt : 0
  const terminal = !connected && live.state !== 'idle' && live.state !== 'running'
  const resumable = terminal && (live.state === 'failed' || live.state === 'cancelled')
  const pendingApproval = pendingApprovals(live.events)[0]
  const budgetRemaining = approvalBudget(audit?.workflow?.budget, live.totalCostUsd, live.totalTokens)

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      {pendingApproval && (
        <div className="flex max-w-[min(44rem,calc(100vw_-_1rem))] min-w-0 items-center gap-2 rounded border border-amber-300 bg-amber-50 px-2 py-1 text-[11px] text-amber-900">
          <div className="min-w-0">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
              <span className="max-w-56 truncate font-medium" title={pendingApproval.summary}>
                {pendingApproval.summary}
              </span>
              {pendingApproval.paths.length > 0 && (
                <span className="max-w-48 truncate text-amber-800" title={pendingApproval.paths.join(', ')}>
                  {pendingApproval.paths.join(', ')}
                </span>
              )}
              {pendingApproval.preview && (
                <code className="max-w-64 truncate rounded bg-white/70 px-1 py-0.5 text-[10px] text-amber-950">
                  {pendingApproval.preview}
                </code>
              )}
              {budgetRemaining && <span className="text-amber-800">{budgetRemaining}</span>}
            </div>
            {pendingApproval.diff && (
              <pre className="mt-1 max-h-16 max-w-[38rem] overflow-auto whitespace-pre-wrap rounded bg-white/70 px-1.5 py-1 font-mono text-[10px] leading-4 text-amber-950">
                {pendingApproval.diff}
              </pre>
            )}
          </div>
          <button type="button" className="btn shrink-0" onClick={() => void approve(pendingApproval.id)}>
            Approve
          </button>
          <button type="button" className="btn shrink-0" onClick={() => void reject(pendingApproval.id)}>
            Reject
          </button>
        </div>
      )}
      {connected && (
        <span className="flex items-center gap-1.5 font-mono text-[11px] text-neutral-500" aria-label="run progress">
          {total > 0 && (
            <span className="hidden h-1 w-16 overflow-hidden rounded bg-neutral-200 md:block" aria-hidden="true">
              <span
                className="block h-full bg-blue-500"
                style={{ width: `${total === 0 ? 0 : Math.round((completed / total) * 100)}%` }}
              />
            </span>
          )}
          <span>
            {completed}/{total}
          </span>
          {idleMs > 3000 && <span className="text-amber-600">working… idle {Math.round(idleMs / 1000)}s</span>}
        </span>
      )}
      {connected && (
        <button type="button" className="btn shrink-0" onClick={() => void cancel()} title="Cancel this run">
          Cancel
        </button>
      )}
      {resumable && (
        <button
          type="button"
          className="btn shrink-0"
          onClick={() => void retry()}
          title="Resume this execution: reuse finished nodes, re-run the rest (does not re-pay for completed work)"
        >
          Resume
        </button>
      )}
      {terminal && (
        <button
          type="button"
          className="btn shrink-0"
          onClick={() => void reexecute()}
          title="Re-run the frozen workflow as a new execution; unchanged nodes are served from cache"
        >
          Re-run
        </button>
      )}
      {terminal && (
        <button
          type="button"
          className="btn shrink-0"
          onClick={() => void clearNodeCache()}
          title="Clear this execution's cached nodes so the next run recomputes them"
        >
          Clear cache
        </button>
      )}
      {execId && (
        <a
          className="btn shrink-0"
          href={bundleUrl(serverUrl, execId)}
          title="Download a portable bundle (snapshot + events + artifacts) for this execution"
          data-testid="export-bundle"
        >
          Export
        </a>
      )}
    </div>
  )
}

interface PendingApproval {
  id: string
  summary: string
  paths: string[]
  preview: string
  diff: string
}

function pendingApprovals(events: { type: string; payload?: Record<string, unknown>; nodeId?: string }[]): PendingApproval[] {
  const pending = new Map<string, PendingApproval>()
  for (const ev of events) {
    const id = typeof ev.payload?.checkpointId === 'string' ? ev.payload.checkpointId : ''
    if (!id) continue
    if (ev.type === 'ApprovalRequested') {
      const mutation = record(ev.payload?.mutation)
      const input = record(ev.payload?.input)
      const summary = stringField(mutation, 'summary') || `Approve ${ev.nodeId ?? 'tool'}`
      const paths = stringArray(mutation?.paths)
      const command = stringArray(mutation?.command).join(' ')
      const method = stringField(mutation, 'method')
      const url = stringField(mutation, 'url')
      const api = method && url ? `${method} ${url}` : ''
      pending.set(id, { id, summary, paths, preview: command || api, diff: fileDiffPreview(paths, input) })
    }
    if (ev.type === 'ApprovalGranted' || ev.type === 'ApprovalRejected') {
      pending.delete(id)
    }
  }
  return [...pending.values()]
}

function record(v: unknown): Record<string, unknown> | null {
  return typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}

function stringField(obj: Record<string, unknown> | null, key: string): string {
  const v = obj?.[key]
  return typeof v === 'string' ? v : ''
}

function stringArray(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((item): item is string => typeof item === 'string') : []
}

function fileDiffPreview(paths: string[], input: Record<string, unknown> | null): string {
  if (stringField(input, 'op') !== 'write') return ''
  const content = stringField(input, 'content')
  if (!content) return ''
  const path = stringField(input, 'path') || paths[0] || 'file'
  const lines = content.split(/\r?\n/)
  const shown = lines.slice(0, 8).map((line) => `+${line}`)
  if (lines.length > shown.length) shown.push('+...')
  return [`--- ${path}`, `+++ ${path}`, ...shown].join('\n')
}

function approvalBudget(
  budget: { maxCostUsd?: number; maxTokens?: number } | undefined,
  spentCostUsd: number,
  spentTokens: number,
): string {
  const parts: string[] = []
  if (budget?.maxCostUsd && budget.maxCostUsd > 0) {
    parts.push(`$${Math.max(0, budget.maxCostUsd - spentCostUsd).toFixed(4)} left`)
  }
  if (budget?.maxTokens && budget.maxTokens > 0) {
    parts.push(`${Math.max(0, budget.maxTokens - spentTokens)} tok left`)
  }
  return parts.join(' / ')
}
