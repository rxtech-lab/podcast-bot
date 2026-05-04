import { useEffect, useReducer, useRef } from 'react'
import { loadHistory, loadTopics } from './api'
import type {
  ChannelInfo,
  ChatLine,
  PhaseEvent,
  StatusEvent,
  TickEvent,
  TopicEvent,
  TranscriptEvent,
} from './types'

// DebateState is what App renders. The runtime always operates in TV-channel
// mode: channels run in parallel; debates within a channel play sequentially.
// `selectedChannelId` is which channel the user is tuned to.
export interface DebateState {
  history: ChatLine[]
  phase: string
  // Server-provided label for the current phase. Display this directly
  // — it's already content-type aware ("問答" vs "自由辯論").
  phaseLabel: string
  elapsedMs: number
  remainingMs: number
  status: string
  channels: ChannelInfo[]
  selectedChannelId: string | null
  // Index of the currently-airing debate within the selected channel's queue.
  selectedDebateIndex: number
  selectedDebateTotal: number
}

type Action =
  | { kind: 'history'; lines: ChatLine[] }
  | { kind: 'append'; line: ChatLine }
  | { kind: 'phase'; phase: string; label: string }
  | { kind: 'tick'; elapsedMs: number; remainingMs: number }
  | { kind: 'status'; text: string }
  | { kind: 'channels'; channels: ChannelInfo[] }
  | { kind: 'select-channel'; id: string }
  | { kind: 'topic-switch'; index: number; total: number }

const initialState: DebateState = {
  history: [],
  phase: 'setup',
  phaseLabel: '',
  elapsedMs: 0,
  remainingMs: 0,
  status: '',
  channels: [],
  selectedChannelId: null,
  selectedDebateIndex: 0,
  selectedDebateTotal: 0,
}

function reducer(state: DebateState, action: Action): DebateState {
  switch (action.kind) {
    case 'history':
      return { ...state, history: action.lines }
    case 'append':
      return { ...state, history: [...state.history, action.line] }
    case 'phase':
      return { ...state, phase: action.phase, phaseLabel: action.label }
    case 'tick':
      return {
        ...state,
        elapsedMs: action.elapsedMs,
        remainingMs: action.remainingMs,
      }
    case 'status':
      return { ...state, status: action.text }
    case 'channels':
      return { ...state, channels: action.channels }
    case 'select-channel':
      // Reset transcript / phase / timers so the UI doesn't briefly show
      // stale data from the previous channel before SSE catches up.
      return {
        ...state,
        history: [],
        phase: 'setup',
        phaseLabel: '',
        elapsedMs: 0,
        remainingMs: 0,
        status: '',
        selectedChannelId: action.id,
        selectedDebateIndex: 0,
        selectedDebateTotal: 0,
      }
    case 'topic-switch':
      // Server pushed a new debate within the user's selected channel —
      // reset the transcript view so we don't merge two debates' chats.
      return {
        ...state,
        history: [],
        phase: 'setup',
        phaseLabel: '',
        elapsedMs: 0,
        remainingMs: 0,
        status: '',
        selectedDebateIndex: action.index,
        selectedDebateTotal: action.total || state.selectedDebateTotal,
      }
  }
}

interface InFlight {
  speaker: string
  role: string
  side: string
  text: string
}

function turnId(s: { role: string; speaker: string; side: string }): string {
  return `${s.role}|${s.speaker}|${s.side || ''}`
}

export function useDebateEvents() {
  const [state, dispatch] = useReducer(reducer, initialState)

  // Refs let SSE event handlers (registered once per subscription) read the
  // current selected channel without forcing a full re-subscribe on every
  // change.
  const selectedRef = useRef(state.selectedChannelId)
  selectedRef.current = state.selectedChannelId

  // (Initial chat history is now loaded inside selectChannel via loadHistory —
  // there used to be a `useEffect(..., [initialHistory])` here that dispatched
  // `{ kind: 'history', lines: initialHistory }`. Because callers passed `[]`
  // inline, every parent render created a new array reference and the effect
  // re-ran, wiping the SSE-appended history back to empty on every tick. The
  // chat would briefly show messages, then visually reset to "no messages
  // yet". Don't reintroduce that param — fetch history on channel-switch.)

  // Refresh the channels list on selection changes so debate statuses
  // (pending → running → done) stay in sync.
  useEffect(() => {
    loadTopics()
      .then((resp) => dispatch({ kind: 'channels', channels: resp.channels }))
      .catch(() => {})
  }, [state.selectedChannelId])

  const selectChannel = (id: string) => {
    if (id === state.selectedChannelId) return
    dispatch({ kind: 'select-channel', id })
    loadHistory(id)
      .then((lines) =>
        dispatch({
          kind: 'history',
          lines: lines.map((l) => ({
            speaker: l.speaker,
            role: l.role,
            side: l.side,
            text: l.text,
          })),
        }),
      )
      .catch(() => {})
  }

  useEffect(() => {
    if (!state.selectedChannelId) return
    // Subscribe to SSE filtered by the selected channel — server drops
    // events from other channels server-side so we don't waste bandwidth.
    const url = `/api/events?channel=${encodeURIComponent(state.selectedChannelId)}`
    const es = new EventSource(url)
    // Diagnostic log: confirms the EventSource opens and which channel it's
    // bound to. If you don't see this in the console after tuning to a
    // channel, the useEffect isn't firing (selectedChannelId is null).
    console.info('[debate] SSE subscribing', { url, channel: state.selectedChannelId })
    es.addEventListener('open', () => {
      console.info('[debate] SSE open', state.selectedChannelId)
    })
    let inFlight: InFlight | null = null

    const accepts = (channelId: string | undefined): boolean => {
      const sel = selectedRef.current
      if (!sel) return false
      // Empty channel_id is broadcast (e.g. setup-phase status messages
      // before any TopicMsg is published); accept those.
      const ok = !channelId || channelId === sel
      if (!ok) {
        console.debug('[debate] SSE event filtered out', {
          eventChannel: channelId,
          selected: sel,
        })
      }
      return ok
    }

    es.addEventListener('transcript', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as TranscriptEvent
      console.debug('[debate] transcript event', m)
      if (!accepts(m.channel_id)) return
      if (m.role === 'user' && m.done) {
        dispatch({
          kind: 'append',
          line: { speaker: m.speaker, role: m.role, side: m.side, text: m.text },
        })
        return
      }
      if (m.text) {
        const id = turnId(m)
        const curId = inFlight ? turnId(inFlight) : null
        if (id !== curId) {
          inFlight = { speaker: m.speaker, role: m.role, side: m.side, text: '' }
        }
        const sep = inFlight!.text.length > 0 ? ' ' : ''
        inFlight!.text += sep + m.text
      }
      if (m.done && inFlight) {
        const text = (m.text || inFlight.text || '').trim()
        if (text) {
          dispatch({
            kind: 'append',
            line: {
              speaker: inFlight.speaker,
              role: inFlight.role,
              side: inFlight.side,
              text,
            },
          })
        }
        inFlight = null
      }
    })

    es.addEventListener('tick', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as TickEvent
      if (!accepts(m.channel_id)) return
      dispatch({
        kind: 'tick',
        elapsedMs: m.elapsed_ms,
        remainingMs: m.remaining_ms,
      })
    })

    es.addEventListener('phase', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as PhaseEvent
      if (!accepts(m.channel_id)) return
      dispatch({ kind: 'phase', phase: m.phase, label: m.label || '' })
    })

    es.addEventListener('status', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as StatusEvent
      if (!accepts(m.channel_id)) return
      dispatch({ kind: 'status', text: m.text || '' })
    })

    es.addEventListener('error', (ev) => {
      try {
        const m = JSON.parse((ev as MessageEvent).data) as { text: string; channel_id?: string }
        if (!accepts(m.channel_id)) return
        dispatch({ kind: 'status', text: 'error: ' + m.text })
      } catch {
        // EventSource fires a generic 'error' on connection drop; payload is undefined.
      }
    })

    es.addEventListener('ended', (ev) => {
      try {
        const m = JSON.parse((ev as MessageEvent).data) as { channel_id?: string }
        if (!accepts(m.channel_id)) return
      } catch {
        // missing payload — fall through
      }
      dispatch({ kind: 'status', text: 'ended' })
    })

    es.addEventListener('topic', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as TopicEvent
      if (!accepts(m.channel_id)) return
      inFlight = null
      dispatch({
        kind: 'topic-switch',
        index: m.index,
        total: m.total,
      })
      // Refetch the persisted transcript snapshot — the orchestrator may
      // have already pushed lines (e.g. host opening) before this event
      // arrives in the browser.
      loadHistory(selectedRef.current ?? undefined)
        .then((lines) =>
          dispatch({
            kind: 'history',
            lines: lines.map((l) => ({
              speaker: l.speaker,
              role: l.role,
              side: l.side,
              text: l.text,
            })),
          }),
        )
        .catch(() => {})
    })

    // The folder watcher discovered a new debate.md and queued it. Refetch
    // /api/topics so the channel switcher shows the freshly-added entry
    // without the viewer needing to reload the page or change channels.
    es.addEventListener('topics_changed', () => {
      loadTopics()
        .then((resp) => dispatch({ kind: 'channels', channels: resp.channels }))
        .catch(() => {})
    })

    es.onerror = (e) => {
      console.warn('[debate] SSE onerror', { readyState: es.readyState, err: e })
      dispatch({ kind: 'status', text: 'reconnecting…' })
    }

    return () => {
      console.info('[debate] SSE closing', state.selectedChannelId)
      es.close()
    }
  }, [state.selectedChannelId])

  return { state, selectChannel }
}
