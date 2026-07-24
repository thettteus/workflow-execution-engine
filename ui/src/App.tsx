import { useEffect, useState } from 'react'

import { Canvas } from './components/Canvas'
import { CommandPalette } from './components/CommandPalette'
import { Inspector } from './components/Inspector'
import { NotificationCenter } from './components/NotificationCenter'
import { ResizeHandle } from './components/ResizeHandle'
import { SettingsModal } from './components/SettingsModal'
import { TabBar } from './components/TabBar'
import { TemplateGallery } from './components/TemplateGallery'
import { Timeline } from './components/Timeline'
import { Toolbar } from './components/Toolbar'
import { usePersistedSize } from './core/resizable'
import { useThemeMode } from './core/theme'
import { useWorkspace } from './store'

// App is the single workspace — one screen, no router, no page navigation
// (VISION UI Philosophy). Toolbar on top, Canvas center, Inspector right,
// Timeline bottom, ⌘K command palette and the Template gallery over
// everything.
export default function App() {
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [galleryOpen, setGalleryOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const theme = useThemeMode()
  const [inspectorWidth, setInspectorWidth] = usePersistedSize(
    'wee.inspectorWidth',
    320,
    240,
    640,
  )
  const [timelineHeight, setTimelineHeight] = usePersistedSize(
    'wee.timelineHeight',
    192,
    120,
    600,
  )
  const [timelineMaximized, setTimelineMaximized] = useState(false)
  const [timelineMinimized, setTimelineMinimized] = useState(false)
  const [inspectorMaximized, setInspectorMaximized] = useState(false)
  const [inspectorMinimized, setInspectorMinimized] = useState(false)
  const selectedNodeId = useWorkspace((s) => s.selectedNodeId)
  const selectNode = useWorkspace((s) => s.selectNode)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!(e.metaKey || e.ctrlKey)) return

      const key = e.key.toLowerCase()
      if (key === 'k') {
        e.preventDefault()
        setPaletteOpen((o) => !o)
        return
      }
      if (isEditableTarget(e.target)) return
      if (key === 'z') {
        e.preventDefault()
        useWorkspace.getState().undo()
        return
      }
      if (key === 'w' || key === 't') {
        e.preventDefault()
        addShortcutNode(key === 'w' ? 'worker' : 'tool')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="app-shell token-shell flex h-screen flex-col">
      <TabBar />
      <Toolbar
        onOpenPalette={() => setPaletteOpen(true)}
        onOpenTemplates={() => setGalleryOpen(true)}
        onOpenSettings={() => setSettingsOpen(true)}
        onOpenHelp={() => setHelpOpen(true)}
        theme={theme.resolved}
        onToggleTheme={theme.toggleTheme}
        notificationsSlot={<NotificationCenter />}
      />
      <div className="flex min-h-0 flex-1">
        <main className="min-w-0 flex-1" aria-label="Canvas">
          <Canvas />
        </main>
        <div className="hidden md:contents">
          {!inspectorMinimized && !inspectorMaximized && (
            <ResizeHandle
              axis="x"
              onDelta={(d) => setInspectorWidth(inspectorWidth - d)}
            />
          )}
          {inspectorMinimized ? (
            <PanelRail
              side="right"
              onRestore={() => setInspectorMinimized(false)}
            />
          ) : (
            <Inspector
              width={inspectorMaximized ? '70vw' : inspectorWidth}
              actions={
                <PanelActions
                  panel="right panel"
                  maximized={inspectorMaximized}
                  onMinimize={() => setInspectorMinimized(true)}
                  onToggleMaximize={() => setInspectorMaximized((m) => !m)}
                />
              }
            />
          )}
        </div>
      </div>
      {!timelineMaximized && !timelineMinimized && (
        <ResizeHandle
          axis="y"
          onDelta={(d) => setTimelineHeight(timelineHeight - d)}
        />
      )}
      {timelineMinimized ? (
        <div className="flex h-9 shrink-0 items-center justify-end border-t border-neutral-200 bg-white px-2">
          <button
            type="button"
            className="btn flex h-7 w-7 items-center justify-center p-0"
            onClick={() => setTimelineMinimized(false)}
            title="Restore bottom panel"
            aria-label="Restore bottom panel"
          >
            <PanelIcon name="expand" />
          </button>
        </div>
      ) : (
        <div
          className="shrink-0"
          style={{ height: timelineMaximized ? '70vh' : timelineHeight }}
        >
          <Timeline
            maximized={timelineMaximized}
            onToggleMaximize={() => setTimelineMaximized((m) => !m)}
            onToggleMinimize={() => {
              setTimelineMaximized(false)
              setTimelineMinimized(true)
            }}
          />
        </div>
      )}
      {selectedNodeId && (
        <div className="fixed inset-x-0 bottom-0 top-20 z-30 md:hidden">
          <button
            type="button"
            className="absolute right-2 top-1 z-10 flex h-7 w-7 items-center justify-center rounded border border-neutral-300 bg-white text-lg leading-none"
            onClick={() => selectNode(null)}
            title="Close node details"
            aria-label="Close node details"
          >
            ×
          </button>
          <Inspector width="100%" />
        </div>
      )}
      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        onOpenTemplates={() => setGalleryOpen(true)}
        onOpenSettings={() => setSettingsOpen(true)}
        onOpenHelp={() => setHelpOpen(true)}
        onToggleTheme={theme.toggleTheme}
      />
      <TemplateGallery open={galleryOpen} onOpenChange={setGalleryOpen} />
      <SettingsModal open={settingsOpen} onOpenChange={setSettingsOpen} />
      {helpOpen && <HelpModal onClose={() => setHelpOpen(false)} />}
    </div>
  )
}

function addShortcutNode(kind: 'worker' | 'tool') {
  const workspace = useWorkspace.getState()
  const id = uniqueNodeId(kind, new Set(workspace.nodes.map((n) => n.id)))
  workspace.addNode(
    kind === 'worker'
      ? { id, worker: `${id}@1.0.0` }
      : { id, tool: { toolName: 'terminal', input: {} } },
  )
}

function uniqueNodeId(kind: string, taken: Set<string>): string {
  let i = 1
  while (taken.has(`${kind}-${i}`)) i++
  return `${kind}-${i}`
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName.toLowerCase()
  return (
    tag === 'input' ||
    tag === 'textarea' ||
    tag === 'select' ||
    target.isContentEditable
  )
}

function PanelActions({
  panel,
  maximized,
  onMinimize,
  onToggleMaximize,
}: {
  panel: string
  maximized: boolean
  onMinimize: () => void
  onToggleMaximize: () => void
}) {
  return (
    <div className="flex items-center gap-1">
      <button
        type="button"
        className="btn flex h-6 w-6 items-center justify-center p-0"
        onClick={onMinimize}
        title={`Minimize ${panel}`}
        aria-label={`Minimize ${panel}`}
      >
        <PanelIcon name="minimize" />
      </button>
      <button
        type="button"
        className="btn flex h-6 w-6 items-center justify-center p-0"
        onClick={onToggleMaximize}
        title={maximized ? `Restore ${panel}` : `Maximize ${panel}`}
        aria-label={maximized ? `Restore ${panel}` : `Maximize ${panel}`}
      >
        <PanelIcon name="expand" />
      </button>
    </div>
  )
}

function PanelRail({
  side,
  onRestore,
}: {
  side: 'right'
  onRestore: () => void
}) {
  return (
    <div className="flex w-9 shrink-0 items-start justify-center border-l border-neutral-200 bg-white p-1">
      <button
        type="button"
        className="btn flex h-7 w-7 items-center justify-center p-0"
        onClick={onRestore}
        title={`Restore ${side} panel`}
        aria-label={`Restore ${side} panel`}
      >
        <PanelIcon name="expand" />
      </button>
    </div>
  )
}

function PanelIcon({ name }: { name: 'minimize' | 'expand' }) {
  if (name === 'minimize') {
    return (
      <svg viewBox="0 0 16 16" className="h-4 w-4" aria-hidden="true">
        <path
          d="M4 8h8"
          fill="none"
          stroke="currentColor"
          strokeLinecap="round"
          strokeWidth="1.5"
        />
      </svg>
    )
  }
  return (
    <svg viewBox="0 0 16 16" className="h-4 w-4" aria-hidden="true">
      <path
        d="M3.5 6V3.5H6M10 3.5h2.5V6M12.5 10v2.5H10M6 12.5H3.5V10"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.5"
      />
    </svg>
  )
}

function HelpModal({ onClose }: { onClose: () => void }) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/25 pt-24"
      onClick={onClose}
    >
      <div
        className="token-card w-[42rem] max-w-[94vw] overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-neutral-200 px-3 py-2.5">
          <div>
            <div className="text-sm font-semibold">WEE help</div>
            <div className="token-subtle text-xs">
              versioned with this running UI
            </div>
          </div>
          <button type="button" className="btn" onClick={onClose}>
            close
          </button>
        </div>
        <div className="grid gap-3 p-3 text-sm md:grid-cols-3">
          <HelpCard
            title="Quickstart"
            body="Import a workflow or pick a template, configure Connections and runtime defaults, then Run."
          />
          <HelpCard
            title="Core concepts"
            body="Contracts define output shape; Context Policy controls admitted artifacts; cache hits reuse recorded artifacts."
          />
          <HelpCard
            title="Keyboard"
            body="Use Cmd/Ctrl+K for commands, Cmd/Ctrl+Z for undo, Cmd/Ctrl+W for worker nodes, and Cmd/Ctrl+T for tool nodes."
          />
        </div>
      </div>
    </div>
  )
}

function HelpCard({ title, body }: { title: string; body: string }) {
  return (
    <div className="rounded border border-neutral-200 bg-neutral-50 p-2">
      <div className="font-medium text-neutral-900">{title}</div>
      <p className="mt-1 text-xs text-neutral-600">{body}</p>
    </div>
  )
}
