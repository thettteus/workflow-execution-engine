import { signal } from '../core/status'
import { useLive } from '../liveStore'
import { TabItem } from './TabItem'

export function RunTabs() {
  const tabs = useLive((s) => s.tabs)
  const activeTabId = useLive((s) => s.activeTabId)
  const switchTab = useLive((s) => s.switchTab)
  const closeTab = useLive((s) => s.closeTab)

  if (tabs.length === 0) return null

  return (
    <>
      {tabs.map((tab) => {
        const active = tab.id === activeTabId
        const state = tab.live.state
        const tabSignal = signal(tab.connected ? 'watching' : state)
        return (
          <TabItem
            key={tab.id}
            active={active}
            maxWidthClassName="max-w-52"
            onSelect={() => switchTab(tab.id)}
            selectTitle={tab.id}
            leading={
              <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${tabSignal.dotClass}`} aria-hidden="true" />
            }
            label={tab.label}
            labelClassName="font-mono"
            onClose={() => closeTab(tab.id)}
            closeTitle="Close run tab"
            closeAriaLabel={`Close run ${tab.id}`}
          />
        )
      })}
    </>
  )
}
