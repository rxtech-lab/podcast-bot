import {
  CalendarBlank,
  CaretDoubleLeft,
  CaretDoubleRight,
  CheckCircle,
  Circle,
  PlayCircle,
  Spinner,
  Television,
  Warning,
  WifiSlash,
} from '@phosphor-icons/react'
import type { ChannelInfo, Session, SessionStatus } from '@/lib/types'

interface ChannelSwitcherProps {
  channels: ChannelInfo[]
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

function airingDebate(channel: ChannelInfo) {
  if (!channel.current_debate_id) return undefined
  return channel.debates.find((d) => d.id === channel.current_debate_id)
}

export function ChannelSwitcher({
  channels,
  currentChannelId,
  onSelect,
  onCollapse,
}: ChannelSwitcherProps) {
  return (
    <aside className="w-full md:w-[240px] md:flex-shrink-0 flex flex-col bg-card/70 backdrop-blur-md border border-border/50 rounded-2xl overflow-hidden shadow-2xl shadow-black/40">
      <header className="flex items-center gap-2.5 px-3.5 py-3 border-b border-border/40 bg-gradient-to-b from-card/40 to-transparent">
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary/15 ring-1 ring-primary/30">
          <Television weight="duotone" className="h-4 w-4 text-primary" />
        </div>
        <div className="flex flex-col leading-tight min-w-0 flex-1">
          <span className="text-sm font-semibold">channels</span>
          <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
            {channels.length} channel{channels.length === 1 ? '' : 's'}
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

      <ol className="m-0 p-2 list-none flex flex-col gap-1.5 overflow-y-auto flex-1 min-h-0">
        {channels.length === 0 && (
          <li className="text-xs text-muted-foreground px-2 py-3 text-center">
            no channels configured
          </li>
        )}
        {channels.map((c) => {
          const isCurrent = c.id === currentChannelId
          const airing = airingDebate(c)
          const status: SessionStatus = c.off_air
            ? 'pending'
            : (airing?.status ?? 'pending')
          const badgeClass = statusBadge[status]
          return (
            <li key={c.id}>
              <button
                type="button"
                onClick={() => onSelect(c.id)}
                title={c.title}
                className={`group w-full flex items-start gap-2 rounded-xl px-2.5 py-2 text-left transition-colors ${
                  isCurrent
                    ? 'bg-primary/15 ring-1 ring-primary/40'
                    : 'hover:bg-muted/30'
                }`}
              >
                <span
                  className={`mt-0.5 inline-flex shrink-0 items-center justify-center rounded-md p-0.5 ring-1 ${badgeClass}`}
                >
                  {c.off_air ? (
                    <WifiSlash
                      weight="duotone"
                      className="h-3.5 w-3.5 text-muted-foreground"
                    />
                  ) : (
                    <StatusIcon status={status} />
                  )}
                </span>
                <span className="flex flex-col min-w-0 leading-tight flex-1">
                  <span className="flex items-center gap-1.5 text-[10px] uppercase tracking-widest text-muted-foreground">
                    <span className="tabular-nums">ch {c.number}</span>
                    {isCurrent && (
                      <span className="text-primary/80">· tuned</span>
                    )}
                    {c.off_air && <span>· off air</span>}
                  </span>
                  <span className="text-xs font-semibold truncate text-foreground/90">
                    {c.title}
                  </span>
                  {airing && (
                    <span className="text-[10px] text-muted-foreground truncate">
                      now: {airing.title}
                    </span>
                  )}
                  {!c.off_air && !airing && c.debates.length > 0 && (
                    <span className="text-[10px] text-muted-foreground truncate">
                      {c.debates.length} debate{c.debates.length === 1 ? '' : 's'} queued
                    </span>
                  )}
                </span>
              </button>
            </li>
          )
        })}
      </ol>

    </aside>
  )
}

// ChannelSchedule renders the tuned channel's queue as a horizontal "TV
// guide" strip designed to sit above the video element. Hidden when the user
// hasn't tuned to a channel yet, when the channel is off-air, or when no
// debates are queued — in those cases there's nothing useful to show and the
// extra row would just steal vertical space from the video.
export function ChannelSchedule({ channel }: { channel?: ChannelInfo }) {
  if (!channel || channel.off_air || channel.debates.length === 0) return null

  const currentIdx = channel.current_debate_id
    ? channel.debates.findIndex((d) => d.id === channel.current_debate_id)
    : -1

  const now =
    currentIdx >= 0
      ? channel.debates[currentIdx]
      : channel.debates.find((d) => d.status === 'running')

  // Upcoming = everything after the current debate that hasn't finished.
  // When nothing is airing yet, show all pending debates as upcoming.
  const upcoming =
    currentIdx >= 0
      ? channel.debates.slice(currentIdx + 1)
      : channel.debates.filter((d) => d.status === 'pending')

  // If there's literally nothing to surface (no current, no upcoming) skip
  // the strip — the empty case used to read "all debates have finished" but
  // that's just noise above the video.
  if (!now && upcoming.length === 0) return null

  return (
    <div className="flex items-center gap-3 overflow-x-auto rounded-xl border border-border/40 bg-card/60 backdrop-blur-md px-3 py-2 shadow-lg shadow-black/30">
      <div className="flex items-center gap-1.5 flex-shrink-0">
        <CalendarBlank
          weight="duotone"
          className="h-3.5 w-3.5 text-muted-foreground"
        />
        <span className="text-[10px] uppercase tracking-widest text-muted-foreground font-semibold">
          ch {channel.number} · schedule
        </span>
      </div>
      <div className="flex items-center gap-2 min-w-0">
        {now && <ScheduleChip label="now" tone="now" debate={now} />}
        {upcoming.length > 0 && (
          <ScheduleChip label="next" tone="next" debate={upcoming[0]} />
        )}
        {upcoming.slice(1).map((d) => (
          <ScheduleChip key={d.id} label="later" tone="later" debate={d} />
        ))}
      </div>
    </div>
  )
}

const toneStyle: Record<'now' | 'next' | 'later', string> = {
  now: 'text-violet-300 ring-violet-500/40 bg-violet-500/15',
  next: 'text-sky-300 ring-sky-500/40 bg-sky-500/15',
  later: 'text-muted-foreground ring-border/50 bg-muted/40',
}

const labelTone: Record<'now' | 'next' | 'later', string> = {
  now: 'text-violet-300',
  next: 'text-sky-300',
  later: 'text-muted-foreground',
}

function ScheduleChip({
  label,
  tone,
  debate,
}: {
  label: string
  tone: 'now' | 'next' | 'later'
  debate: Session
}) {
  const Icon =
    debate.status === 'running'
      ? PlayCircle
      : debate.status === 'done'
        ? CheckCircle
        : debate.status === 'error'
          ? Warning
          : Circle
  return (
    <div
      className={`flex items-center gap-2 rounded-lg px-2.5 py-1.5 ring-1 ${toneStyle[tone]} min-w-0 max-w-[260px]`}
      title={debate.title}
    >
      <Icon weight="duotone" className="h-3.5 w-3.5 flex-shrink-0" />
      <div className="flex flex-col leading-tight min-w-0">
        <span
          className={`text-[9px] uppercase tracking-widest font-semibold ${labelTone[tone]}`}
        >
          {label}
        </span>
        <span className="text-xs text-foreground/90 truncate">
          {debate.title}
        </span>
      </div>
    </div>
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
