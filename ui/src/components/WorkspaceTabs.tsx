import { useLive } from '../liveStore'
import { useWorkspace } from '../store'
import { TabItem } from './TabItem'

export function WorkspaceTabs() {
  const documents = useWorkspace((s) => s.documents)
  const activeDocumentId = useWorkspace((s) => s.activeDocumentId)
  const switchDocument = useWorkspace((s) => s.switchDocument)
  const closeDocument = useWorkspace((s) => s.closeDocument)
  const newDocument = useWorkspace((s) => s.newDocument)
  const liveState = useLive((s) => s.live.state)
  const running = liveState === 'running'

  function requestClose(id: string) {
    const doc = documents.find((d) => d.id === id)
    if (!doc) return
    if ((doc.dirty || running) && !window.confirm('Close this workflow document? Unsaved edits or a running execution may be lost from view.')) {
      return
    }
    closeDocument(id)
  }

  return (
    <>
      {documents.map((doc) => {
        const active = doc.id === activeDocumentId
        return (
          <TabItem
            key={doc.id}
            active={active}
            onSelect={() => switchDocument(doc.id)}
            selectTitle={doc.label}
            leading={<span aria-hidden="true">{doc.dirty ? '●' : '□'}</span>}
            label={doc.label}
            onClose={() => requestClose(doc.id)}
            closeTitle="Close document"
            closeAriaLabel={`Close ${doc.label}`}
            closeDisabled={documents.length <= 1}
          />
        )
      })}
      <button
        type="button"
        className="flex h-8 w-8 shrink-0 items-center justify-center border border-neutral-200 bg-neutral-50 text-sm text-neutral-600 hover:bg-white"
        onClick={newDocument}
        title="New workflow document"
        aria-label="New workflow document"
      >
        +
      </button>
    </>
  )
}
