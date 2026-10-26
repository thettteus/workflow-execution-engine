import { describe, expect, it } from 'vitest'

import { bars, emptyLive, reduceAll, type WFEvent } from './live'

const execId = 'wf-20260718T000000-aaaa'

function ev(partial: Partial<WFEvent> & Pick<WFEvent, 'type'>): WFEvent {
  return {
    executionId: execId,
    timestamp: '2026-07-18T00:00:00.000Z',
    prevHash: 'x',
    ...partial,
  }
}

describe('live reduce', () => {
  it('starts idle and moves to running on ExecutionStarted', () => {
    const live = reduceAll(
      [ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } })],
      ['a'],
    )
    expect(live.state).toBe('running')
    expect(live.executionId).toBe(execId)
    expect(live.workflow).toBe('wf')
    expect(live.nodes.a.status).toBe('pending')
  })

  it('follows a fresh success sequence: started -> cache miss -> artifact -> finished', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'CacheMiss', nodeId: 'a', payload: { key: 'k' } }),
        ev({ type: 'ArtifactCreated', nodeId: 'a', payload: { hash: 'h1', type: 'json' } }),
        ev({ type: 'WorkerFinished', nodeId: 'a', payload: { costUsd: 0.02, tokens: 10 } }),
        ev({ type: 'ExecutionFinished', payload: { state: 'succeeded' } }),
      ],
      ['a'],
    )
    expect(live.nodes.a.status).toBe('succeeded')
    expect(live.nodes.a.cached).toBe(false)
    expect(live.totalCostUsd).toBeCloseTo(0.02)
    expect(live.totalTokens).toBe(10)
    expect(live.artifacts).toEqual([{ nodeId: 'a', hash: 'h1', type: 'json', at: expect.any(Number) }])
    expect(live.state).toBe('succeeded')
  })

  it('marks a node cached and tracks avoided spend on a cache hit', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'CacheHit', nodeId: 'a', payload: { key: 'k', savedCostUsd: 0.05, savedTokens: 100 } }),
        ev({ type: 'ArtifactCreated', nodeId: 'a', payload: { hash: 'h1', type: 'json' } }),
        ev({ type: 'WorkerFinished', nodeId: 'a', payload: { costUsd: 0, tokens: 0 } }),
        ev({ type: 'ExecutionFinished', payload: { state: 'succeeded' } }),
      ],
      ['a'],
    )
    expect(live.nodes.a.status).toBe('cached')
    expect(live.nodes.a.cached).toBe(true)
    expect(live.savedCostUsd).toBeCloseTo(0.05)
    expect(live.savedTokens).toBe(100)
    expect(live.totalCostUsd).toBe(0)
  })

  it('marks a node failed on Failure with no WorkerFinished', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'Failure', nodeId: 'a', payload: { error: 'boom' } }),
        ev({ type: 'ExecutionFinished', payload: { state: 'failed' } }),
      ],
      ['a'],
    )
    expect(live.nodes.a.status).toBe('failed')
    expect(live.nodes.a.error).toBe('boom')
    expect(live.state).toBe('failed')
  })

  it('counts the billed spend a Failure reports for rejected attempts', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'WorkerFinished', nodeId: 'a', payload: { costUsd: 0.01, tokens: 5 } }),
        ev({ type: 'WorkerStarted', nodeId: 'b' }),
        ev({ type: 'Failure', nodeId: 'b', payload: { error: 'contract violation', costUsd: 0.03, tokens: 30 } }),
        ev({ type: 'ExecutionFinished', payload: { state: 'failed' } }),
      ],
      ['a', 'b'],
    )
    expect(live.nodes.b.costUsd).toBeCloseTo(0.03)
    expect(live.nodes.b.tokens).toBe(30)
    expect(live.totalCostUsd).toBeCloseTo(0.04)
    expect(live.totalTokens).toBe(35)
  })

  it('counts retries', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'Retry', nodeId: 'a', payload: { attempt: 1 } }),
        ev({ type: 'Retry', nodeId: 'a', payload: { attempt: 2 } }),
        ev({ type: 'WorkerFinished', nodeId: 'a', payload: { costUsd: 0, tokens: 0 } }),
      ],
      ['a'],
    )
    expect(live.nodes.a.retries).toBe(2)
  })

  it('reflects cancellation', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'Cancelled' }),
        ev({ type: 'ExecutionFinished', payload: { state: 'cancelled' } }),
      ],
      ['a'],
    )
    expect(live.state).toBe('cancelled')
  })

  it('reflects a pending approval pause', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'ApprovalRequested', nodeId: 'a', payload: { checkpointId: 'cp1' } }),
        ev({ type: 'ExecutionFinished', payload: { state: 'paused' } }),
      ],
      ['a'],
    )
    expect(live.state).toBe('paused')
    expect(live.nodes.a.status).toBe('paused')
  })

  it('leaves nodes untouched by ToolCalled/ToolResult/ContractValidated (recorded, no state change)', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a' }),
        ev({ type: 'ToolCalled', nodeId: 'a', payload: { tool: 'terminal' } }),
        ev({ type: 'ToolResult', nodeId: 'a', payload: { tool: 'terminal' } }),
        ev({ type: 'ContractValidated', nodeId: 'a' }),
      ],
      ['a'],
    )
    expect(live.nodes.a.status).toBe('running')
    expect(live.events).toHaveLength(5)
  })

  it('is order-independent per node id: a node not yet started stays pending', () => {
    const live = reduceAll(
      [ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1.0.0' } }), ev({ type: 'WorkerStarted', nodeId: 'a' })],
      ['a', 'b'],
    )
    expect(live.nodes.a.status).toBe('running')
    expect(live.nodes.b.status).toBe('pending')
  })
})

describe('emptyLive', () => {
  it('seeds every node id as pending and starts idle', () => {
    const live = emptyLive(['a', 'b'])
    expect(live.state).toBe('idle')
    expect(live.nodes.a.status).toBe('pending')
    expect(live.nodes.b.status).toBe('pending')
  })
})

describe('bars', () => {
  it('returns no bars before the run starts', () => {
    expect(bars(emptyLive(['a']), 0)).toEqual([])
  })

  it('computes fractional bars, extending a still-running node to now', () => {
    const live = reduceAll(
      [
        ev({ type: 'ExecutionStarted', timestamp: '2026-07-18T00:00:00.000Z', payload: { workflow: 'wf', version: '1' } }),
        ev({ type: 'WorkerStarted', nodeId: 'a', timestamp: '2026-07-18T00:00:00.000Z' }),
        ev({ type: 'WorkerFinished', nodeId: 'a', timestamp: '2026-07-18T00:00:01.000Z', payload: { costUsd: 0, tokens: 0 } }),
        ev({ type: 'WorkerStarted', nodeId: 'b', timestamp: '2026-07-18T00:00:01.000Z' }),
      ],
      ['a', 'b'],
    )
    const now = Date.parse('2026-07-18T00:00:02.000Z')
    const result = bars(live, now)
    expect(result).toHaveLength(2)
    const a = result.find((b) => b.id === 'a')!
    const b = result.find((b) => b.id === 'b')!
    expect(a.left).toBeCloseTo(0)
    expect(a.width).toBeCloseTo(0.5)
    expect(b.left).toBeCloseTo(0.5)
    expect(b.width).toBeCloseTo(0.5)
  })

  it('excludes nodes that never started (pending or skipped)', () => {
    const live = reduceAll(
      [ev({ type: 'ExecutionStarted', payload: { workflow: 'wf', version: '1' } }), ev({ type: 'WorkerStarted', nodeId: 'a' })],
      ['a', 'b'],
    )
    const result = bars(live, Date.now())
    expect(result.map((b) => b.id)).toEqual(['a'])
  })
})
