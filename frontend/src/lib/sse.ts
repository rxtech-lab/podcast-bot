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
  | { kind: 'phase'; phase: string }
  | { kind: 'tick'; elapsedMs: number; remainingMs: number }
  | { kind: 'status'; text: string }
  | { kind: 'channels'; channels: ChannelInfo[] }
  | { kind: 'select-channel'; id: string }
  | { kind: 'topic-switch'; index: number; total: number }

const initialState: DebateState = {
  history: [],
  phase: 'setup',
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
      return { ...state, phase: action.phase }
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

export function useDebateEvents(initialHistory: ChatLine[]) {
  const [state, dispatch] = useReducer(reducer, initialState)

  // Refs let SSE event handlers (registered once per subscription) read the
  // current selected channel without forcing a full re-subscribe on every
  // change.
  const selectedRef = useRef(state.selectedChannelId)
  selectedRef.current = state.selectedChannelId

  useEffect(() => {
    dispatch({ kind: 'history', lines: initialHistory })
  }, [initialHistory])

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
    let inFlight: InFlight | null = null

    const accepts = (channelId: string | undefined): boolean => {
      const sel = selectedRef.current
      if (!sel) return false
      // Empty channel_id is broadcast (e.g. setup-phase status messages
      // before any TopicMsg is published); accept those.
      return !channelId || channelId === sel
    }

    es.addEventListener('transcript', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as TranscriptEvent
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
      dispatch({ kind: 'phase', phase: m.phase })
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

    es.onerror = () => {
      dispatch({ kind: 'status', text: 'reconnecting…' })
    }

    return () => {
      es.close()
    }
  }, [state.selectedChannelId])

  return { state, selectChannel }
}
