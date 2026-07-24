import { useLive } from '../liveStore'
import { RunTabs } from './RunTabs'
import { WorkspaceTabs } from './WorkspaceTabs'

// TabBar owns the single top tab row: open workflow documents and open
// executions are different concepts backed by different stores, but render
// as one row with a divider between the two groups — this component is the
// one place that decides whether that divider shows, instead of a parent
// (App.tsx) reaching into both children's presence to lay them out.
export function TabBar() {
  const hasRunTabs = useLive((s) => s.tabs.length > 0)

  return (
    <div className="flex h-9 min-w-0 items-end gap-1 border-b border-neutral-200 bg-neutral-100 px-2 pt-1 md:px-3">
      <div className="flex min-w-0 flex-1 items-end gap-1 overflow-x-auto">
        <WorkspaceTabs />
        {hasRunTabs && <div className="mx-1 h-6 w-px shrink-0 self-center bg-neutral-300" aria-hidden="true" />}
        <RunTabs />
      </div>
    </div>
  )
}
