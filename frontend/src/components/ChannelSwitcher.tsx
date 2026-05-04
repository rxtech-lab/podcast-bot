import {
  CaretDoubleLeft,
  CaretDoubleRight,
  CheckCircle,
  Circle,
  Spinner,
  Television,
  Warning,
} from '@phosphor-icons/react'
import type { Mode, Session, SessionStatus } from '@/lib/types'

interface ChannelSwitcherProps {
  topics: Session[]
  mode: Mode
  currentChannelId: string | null
  onSelect: (id: string) => void
  onCollapse: () => void
}

const statusBadge: Record<SessionStatus, string> = {
  pending: 'bg-muted/40 text-muted-foreground ring-border/50',
  running: 'bg-violet-500/15 text-violet-300 ring-violet-500/40',
  done: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/40',
  error: 'bg-red-500/15 text-red-300 ring-red-500/40',
}

function StatusIcon({ status }: { status: SessionStatus }) {
  switch (status) {
    case 'running':
      return (
        <Spinner
          weight="bold"
          className="h-3.5 w-3.5 text-violet-300 animate-spin"
        />
      )
    case 'done':
      return (
        <CheckCircle
          weight="duotone"
          className="h-3.5 w-3.5 text-emerald-300"
        />
      )
    case 'error':
      return <Warning weight="duotone" className="h-3.5 w-3.5 text-red-300" />
    default:
      return (
        <Circle
          weight="duotone"
          className="h-3.5 w-3.5 text-muted-foreground"
        />
      )
  }
}

export function ChannelSwitcher({
  topics,
  mode,
  currentChannelId,
  onSelect,
  onCollapse,
}: ChannelSwitcherProps) {
  return (
    <aside className="w-full md:w-[220px] md:flex-shrink-0 flex flex-col bg-card/70 backdrop-blur-md border border-border/50 rounded-2xl overflow-hidden shadow-2xl shadow-black/40">
      <header className="flex items-center gap-2.5 px-3.5 py-3 border-b border-border/40 bg-gradient-to-b from-card/40 to-transparent">
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary/15 ring-1 ring-primary/30">
          <Television weight="duotone" className="h-4 w-4 text-primary" />
        </div>
        <div className="flex flex-col leading-tight min-w-0 flex-1">
          <span className="text-sm font-semibold">channels</span>
          <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
            {mode === 'parallel' ? 'live · parallel' : 'queued · sequential'}
          </span>
        </div>
        <button
          type="button"
          onClick={onCollapse}
          aria-label="hide channels panel"
          title="hide channels"
          className="flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground hover:bg-muted/40 hover:text-foreground transition-colors"
        >
          <CaretDoubleLeft weight="bold" className="h-3.5 w-3.5" />
        </button>
      </header>

      <ol className="m-0 p-2 list-none flex flex-col gap-1.5 overflow-y-auto">
        {topics.length === 0 && (
          <li className="text-xs text-muted-foreground px-2 py-3 text-center">
            no debates loaded
          </li>
        )}
        {topics.map((t, i) => {
          const isCurrent = t.id === currentChannelId
          const badgeClass = statusBadge[t.status] ?? statusBadge.pending
          return (
            <li key={t.id}>
              <button
                type="button"
                onClick={() => onSelect(t.id)}
                title={t.title}
                className={`group w-full flex items-start gap-2 rounded-xl px-2.5 py-2 text-left transition-colors ${
                  isCurrent
                    ? 'bg-primary/15 ring-1 ring-primary/40'
                    : 'hover:bg-muted/30'
                }`}
              >
                <span
                  className={`mt-0.5 inline-flex shrink-0 items-center justify-center rounded-md p-0.5 ring-1 ${badgeClass}`}
                >
                  <StatusIcon status={t.status} />
                </span>
                <span className="flex flex-col min-w-0 leading-tight">
                  <span className="flex items-center gap-1.5 text-[10px] uppercase tracking-widest text-muted-foreground">
                    <span className="tabular-nums">ch {i + 1}</span>
                    {isCurrent && (
                      <span className="text-primary/80">· tuned</span>
                    )}
                  </span>
                  <span className="text-xs font-medium truncate text-foreground/90">
                    {t.title}
                  </span>
                </span>
              </button>
            </li>
          )
        })}
      </ol>
    </aside>
  )
}

interface ChannelSwitcherToggleProps {
  onExpand: () => void
}

// ChannelSwitcherToggle is the slim re-open button shown in place of the
// switcher when the user has collapsed it.
export function ChannelSwitcherToggle({ onExpand }: ChannelSwitcherToggleProps) {
  return (
    <button
      type="button"
      onClick={onExpand}
      aria-label="show channels panel"
      title="show channels"
      className="md:flex-shrink-0 flex md:flex-col flex-row items-center justify-center gap-2 px-2 py-3 md:w-9 md:h-auto h-9 w-full bg-card/70 backdrop-blur-md border border-border/50 rounded-2xl text-muted-foreground hover:bg-muted/40 hover:text-foreground transition-colors shadow-2xl shadow-black/40"
    >
      <CaretDoubleRight weight="bold" className="h-4 w-4" />
      <Television weight="duotone" className="h-4 w-4" />
    </button>
  )
}
