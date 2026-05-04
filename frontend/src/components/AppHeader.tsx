import { Broadcast } from '@phosphor-icons/react'

function fmtMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0
  const total = Math.floor(ms / 1000)
  const s = total % 60
  const m = Math.floor(total / 60) % 60
  const h = Math.floor(total / 3600)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

const phaseLabel: Record<string, string> = {
  setup: 'setting up',
  opening: 'opening statements',
  'free-debate': 'free debate',
  closing: 'closing statements',
  verdict: "judge's verdict",
  conclusion: 'conclusion',
  ended: 'ended',
}

const phaseStyle: Record<string, string> = {
  setup:
    'bg-muted/60 text-muted-foreground ring-border',
  opening:
    'bg-sky-500/15 text-sky-300 ring-sky-500/30',
  'free-debate':
    'bg-violet-500/15 text-violet-300 ring-violet-500/30',
  closing:
    'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  verdict:
    'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  conclusion:
    'bg-primary/20 text-primary ring-primary/40',
  ended:
    'bg-muted/60 text-muted-foreground ring-border',
}

interface AppHeaderProps {
  phase: string
  elapsedMs: number
  remainingMs: number
  status: string
  currentTopicIndex: number
  totalTopics: number
}

export function AppHeader({
  phase,
  elapsedMs,
  remainingMs,
  status,
  currentTopicIndex,
  totalTopics,
}: AppHeaderProps) {
  const total = elapsedMs + remainingMs
  const pct = total > 0 ? Math.min(100, (elapsedMs / total) * 100) : 0
  const isLive = phase !== 'setup' && phase !== 'ended'
  const label = phaseLabel[phase] ?? phase
  const pillClass = phaseStyle[phase] ?? phaseStyle.setup

  return (
    <header className="px-4 sm:px-5 pt-3 pb-3 border-b border-border/40 bg-gradient-to-b from-card/40 to-transparent backdrop-blur-sm">
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary/15 ring-1 ring-primary/30">
            <Broadcast weight="duotone" className="h-5 w-5 text-primary" />
          </div>
          <div className="flex flex-col leading-tight min-w-0">
            <span className="text-sm font-semibold tracking-tight">
              debate bot
            </span>
            <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
              {totalTopics > 1
                ? `topic ${currentTopicIndex + 1} of ${totalTopics}`
                : 'live broadcast'}
            </span>
          </div>
        </div>

        {isLive && (
          <span className="hidden sm:inline-flex items-center gap-1.5 rounded-full bg-red-500/15 px-2.5 py-0.5 text-[10px] font-bold tracking-widest uppercase text-red-400 ring-1 ring-red-500/40">
            <span className="relative flex h-1.5 w-1.5">
              <span className="absolute inset-0 rounded-full bg-red-500 animate-ping" />
              <span className="relative h-1.5 w-1.5 rounded-full bg-red-500" />
            </span>
            live
          </span>
        )}

        <span
          className={`hidden md:inline-flex items-center rounded-full px-3 py-1 text-xs font-medium ring-1 ${pillClass}`}
        >
          {label}
        </span>

        <span className="ml-auto truncate text-xs italic text-muted-foreground max-w-[40%]">
          {status}
        </span>

        <div className="flex items-center gap-1.5 tabular-nums text-xs text-muted-foreground">
          <span className="text-foreground/90 font-medium">
            {fmtMs(elapsedMs)}
          </span>
          <span className="text-foreground/30">/</span>
          <span>{fmtMs(total)}</span>
        </div>
      </div>

      <div className="mt-2.5 flex items-center gap-2">
        <span
          className={`md:hidden inline-flex items-center rounded-full px-2.5 py-0.5 text-[10px] font-medium ring-1 ${pillClass}`}
        >
          {label}
        </span>
        <div className="flex-1 h-1 overflow-hidden rounded-full bg-muted/40">
          <div
            className="h-full rounded-full bg-gradient-to-r from-primary/60 via-primary to-primary/80 transition-all duration-700 ease-out"
            style={{ width: `${pct}%` }}
          />
        </div>
      </div>

    </header>
  )
}
