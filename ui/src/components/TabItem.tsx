import type { ReactNode } from 'react'

interface TabItemProps {
  active: boolean
  maxWidthClassName?: string
  onSelect: () => void
  selectTitle: string
  leading: ReactNode
  label: string
  labelClassName?: string
  onClose: () => void
  closeTitle: string
  closeAriaLabel: string
  closeDisabled?: boolean
}

// TabItem is the one pill shape shared by RunTabs (open executions) and
// WorkspaceTabs (open workflow documents) — the two are different concepts
// with different stores behind them, but render as tabs in the same bar
// (App.tsx/TabBar.tsx), so they share this markup instead of each keeping
// its own hand-copied version in sync.
export function TabItem({
  active,
  maxWidthClassName = 'max-w-56',
  onSelect,
  selectTitle,
  leading,
  label,
  labelClassName = '',
  onClose,
  closeTitle,
  closeAriaLabel,
  closeDisabled = false,
}: TabItemProps) {
  return (
    <div
      className={`flex h-8 ${maxWidthClassName} shrink-0 items-center gap-1.5 border px-2 text-xs ${
        active
          ? 'border-neutral-200 border-b-white bg-white text-neutral-900'
          : 'border-neutral-200 bg-neutral-50 text-neutral-600'
      }`}
    >
      <button type="button" className="flex min-w-0 items-center gap-1.5" onClick={onSelect} title={selectTitle}>
        {leading}
        <span className={`truncate ${labelClassName}`}>{label}</span>
      </button>
      <button
        type="button"
        className="ml-1 flex h-5 w-5 shrink-0 items-center justify-center rounded text-neutral-400 hover:bg-neutral-200 hover:text-neutral-700"
        onClick={onClose}
        title={closeTitle}
        aria-label={closeAriaLabel}
        disabled={closeDisabled}
      >
        ×
      </button>
    </div>
  )
}
