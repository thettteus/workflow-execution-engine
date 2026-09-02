import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import * as liveClient from '../liveClient'
import { useLive } from '../liveStore'
import { SettingsModal } from './SettingsModal'

describe('SettingsModal', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    useLive.setState({ serverUrl: 'http://127.0.0.1:7676' })
  })

  it('renders nothing when closed', () => {
    render(<SettingsModal open={false} onOpenChange={() => {}} />)
    expect(screen.queryByText('Settings')).not.toBeInTheDocument()
  })

  it('starts clean without expanded provider/env-var rows', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({})
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    render(<SettingsModal open onOpenChange={() => {}} />)

    expect(await screen.findByText('No connections configured yet.')).toBeInTheDocument()
    expect(screen.queryByText('Runtime envs')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('OpenAI API key')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Anthropic API key')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('GitHub token')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Workspace root')).toBeInTheDocument()
    expect(screen.queryByLabelText('Connection preset')).not.toBeInTheDocument()
    expect(screen.getByText('Model providers')).toBeInTheDocument()
    expect(screen.getByText('Change sources')).toBeInTheDocument()
    expect(screen.getByText('Notifications')).toBeInTheDocument()
  })

  it('saves a connection secret, then shows it as set and clears the draft input', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      OPENAI_API_KEY: false,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const setSecretSpy = vi
      .spyOn(liveClient, 'setSecret')
      .mockResolvedValue(undefined)
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Add OpenAI' }))
    const input = await screen.findByLabelText('OpenAI secret')
    fireEvent.change(input, { target: { value: 'sk-live-example' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    // 'Saved.' is the settled signal: it is only set once setSecret has resolved
    // and the success branch ran. Waiting on it first means everything after it
    // is already true and needs no wall-clock budget of its own. Chaining three
    // separate waits here made the test flaky under a loaded full-suite run,
    // where 33 parallel jsdom files can starve a 1s budget.
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
    expect(setSecretSpy).toHaveBeenCalledWith(
      'http://127.0.0.1:7676',
      'OPENAI_API_KEY',
      'sk-live-example',
    )
    expect(input).toHaveValue('')
  })

  it('accepts a raw GitHub token and saves the Authorization header value tools expect', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      GITHUB_AUTH_HEADER: false,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const setSecretSpy = vi
      .spyOn(liveClient, 'setSecret')
      .mockResolvedValue(undefined)
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.click(await screen.findByText('Change sources'))
    fireEvent.click(screen.getByRole('button', { name: 'Add GitHub' }))
    fireEvent.change(await screen.findByLabelText('GitHub secret'), {
      target: { value: 'github_pat_example' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(setSecretSpy).toHaveBeenCalledWith(
        'http://127.0.0.1:7676',
        'GITHUB_AUTH_HEADER',
        'Bearer github_pat_example',
      ),
    )
  })

  it('saves the workspace root as a durable non-secret default', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({})
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const saveSettingsSpy = vi
      .spyOn(liveClient, 'saveSettings')
      .mockImplementation(async (_url, settings) => settings)
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.change(await screen.findByLabelText('Workspace root'), {
      target: { value: '/tmp/bitcoin' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))

    await waitFor(() =>
      expect(saveSettingsSpy.mock.calls.at(-1)?.[1]).toMatchObject({
        workspaceRoot: '/tmp/bitcoin',
      }),
    )
  })

  it('clears a set secret', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      OPENAI_API_KEY: true,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({
      connections: [
        {
          id: 'openai',
          label: 'OpenAI',
          kind: 'model-provider',
          type: 'openai',
          secretEnv: 'OPENAI_API_KEY',
        },
      ],
    })
    const unsetSecretSpy = vi
      .spyOn(liveClient, 'unsetSecret')
      .mockResolvedValue(undefined)
    render(<SettingsModal open onOpenChange={() => {}} />)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Clear' })).toBeEnabled(),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))

    // Same settled-signal rule as the save case above. Waiting on the disabled
    // state was additionally ambiguous: Clear is disabled mid-flight too (while
    // `busy` is set), so that wait could pass on the in-progress state rather
    // than the finished one.
    expect(await screen.findByText('Cleared.')).toBeInTheDocument()
    expect(unsetSecretSpy).toHaveBeenCalledWith(
      'http://127.0.0.1:7676',
      'OPENAI_API_KEY',
    )
    expect(screen.getByRole('button', { name: 'Clear' })).toBeDisabled()
  })

  it('shows a load error instead of a blank panel', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockRejectedValue(
      new Error('boom'),
    )
    vi.spyOn(liveClient, 'fetchSettings').mockRejectedValue(
      new Error('unreachable'),
    )
    render(<SettingsModal open onOpenChange={() => {}} />)
    expect(await screen.findByText('boom')).toBeInTheDocument()
  })

  it('closes with Escape', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({})
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const onOpenChange = vi.fn()
    render(<SettingsModal open onOpenChange={onOpenChange} />)

    expect(await screen.findByRole('dialog', { name: 'Settings' })).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })

    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('loads durable settings and persists edits to disk (REQ-CTRL-05)', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      OPENAI_API_KEY: false,
      ANTHROPIC_API_KEY: false,
      GITHUB_AUTH_HEADER: false,
      WEE_WORKSPACE_ROOT: false,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({ cacheMode: 'on' })
    const saveSpy = vi
      .spyOn(liveClient, 'saveSettings')
      .mockResolvedValue({ cacheMode: 'on', defaultBudgetUsd: 2.5 })
    render(<SettingsModal open onOpenChange={() => {}} />)

    const budget = await screen.findByPlaceholderText(
      "0 = use each workflow's own",
    )
    fireEvent.change(budget, { target: { value: '2.5' } })
    expect(screen.getByText('Unsaved changes.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    expect(screen.getByText('Saving settings…')).toBeInTheDocument()

    await waitFor(() => expect(saveSpy).toHaveBeenCalled())
    expect(saveSpy.mock.calls[0][1]).toMatchObject({ defaultBudgetUsd: 2.5 })
    expect(await screen.findByText('Settings saved.')).toBeInTheDocument()
  })

  it('persists notification rules as non-secret settings', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({})
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const saveSpy = vi
      .spyOn(liveClient, 'saveSettings')
      .mockImplementation(async (_url, settings) => settings)
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.click(await screen.findByText('Notifications'))
    fireEvent.click(screen.getByLabelText('Browser notifications after permission'))
    fireEvent.change(screen.getByLabelText('Quiet hours start'), {
      target: { value: '21:00' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))

    await waitFor(() => expect(saveSpy).toHaveBeenCalled())
    expect(saveSpy.mock.calls.at(-1)?.[1].notifications).toMatchObject({
      browserEnabled: true,
      quietHours: { start: '21:00' },
    })
    expect(JSON.stringify(saveSpy.mock.calls.at(-1)?.[1])).not.toContain('sk-')
  })

  it('adds provider connections from presets and keeps secret values write-only (REQ-CONN-04/05)', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      OPENAI_API_KEY: false,
      ANTHROPIC_API_KEY: false,
      GITHUB_AUTH_HEADER: false,
      WEE_WORKSPACE_ROOT: false,
      MOONSHOT_API_KEY: false,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    const saveSettingsSpy = vi
      .spyOn(liveClient, 'saveSettings')
      .mockImplementation(async (_url, settings) => settings)
    const setSecretSpy = vi
      .spyOn(liveClient, 'setSecret')
      .mockResolvedValue(undefined)
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Add Kimi / Moonshot' }))
    expect(await screen.findByLabelText('kimi label')).toHaveValue(
      'Kimi / Moonshot',
    )

    fireEvent.change(screen.getByLabelText('Kimi / Moonshot secret'), {
      target: { value: 'moonshot-secret' },
    })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' }).at(-1)!)
    await waitFor(() =>
      expect(setSecretSpy).toHaveBeenCalledWith(
        'http://127.0.0.1:7676',
        'MOONSHOT_API_KEY',
        'moonshot-secret',
      ),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }))
    await waitFor(() => expect(saveSettingsSpy).toHaveBeenCalled())
    expect(saveSettingsSpy.mock.calls.at(-1)?.[1].connections?.[0]).toMatchObject({
      id: 'kimi',
      type: 'openai-compatible',
      secretEnv: 'MOONSHOT_API_KEY',
    })
    expect(JSON.stringify(saveSettingsSpy.mock.calls.at(-1)?.[1])).not.toContain(
      'moonshot-secret',
    )
  })

  it('shows durable settings save failures next to the button', async () => {
    vi.spyOn(liveClient, 'fetchSecretsStatus').mockResolvedValue({
      OPENAI_API_KEY: false,
      ANTHROPIC_API_KEY: false,
      GITHUB_AUTH_HEADER: false,
      WEE_WORKSPACE_ROOT: false,
    })
    vi.spyOn(liveClient, 'fetchSettings').mockResolvedValue({})
    vi.spyOn(liveClient, 'saveSettings').mockRejectedValue(
      new Error('disk is read-only'),
    )
    render(<SettingsModal open onOpenChange={() => {}} />)

    fireEvent.click(
      await screen.findByRole('button', { name: 'Save settings' }),
    )

    expect(
      await screen.findByText('Save failed: disk is read-only'),
    ).toBeInTheDocument()
  })
})
