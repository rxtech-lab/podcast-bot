import { useEffect, useReducer } from 'react'
import type {
  ChatLine,
  PhaseEvent,
  StatusEvent,
  TickEvent,
  TranscriptEvent,
} from './types'

export interface DebateState {
  history: ChatLine[]
  phase: string
  elapsedMs: number
  remainingMs: number
  status: string
}

type Action =
  | { kind: 'history'; lines: ChatLine[] }
  | { kind: 'append'; line: ChatLine }
  | { kind: 'phase'; phase: string }
  | { kind: 'tick'; elapsedMs: number; remainingMs: number }
  | { kind: 'status'; text: string }

const initialState: DebateState = {
  history: [],
  phase: 'setup',
  elapsedMs: 0,
  remainingMs: 0,
  status: '',
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

  useEffect(() => {
    dispatch({ kind: 'history', lines: initialHistory })
  }, [initialHistory])

  useEffect(() => {
    const es = new EventSource('/api/events')
    let inFlight: InFlight | null = null

    es.addEventListener('transcript', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as TranscriptEvent
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
      dispatch({
        kind: 'tick',
        elapsedMs: m.elapsed_ms,
        remainingMs: m.remaining_ms,
      })
    })

    es.addEventListener('phase', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as PhaseEvent
      dispatch({ kind: 'phase', phase: m.phase })
    })

    es.addEventListener('status', (ev) => {
      const m = JSON.parse((ev as MessageEvent).data) as StatusEvent
      dispatch({ kind: 'status', text: m.text || '' })
    })

    es.addEventListener('error', (ev) => {
      try {
        const m = JSON.parse((ev as MessageEvent).data) as { text: string }
        dispatch({ kind: 'status', text: 'error: ' + m.text })
      } catch {
        // EventSource fires a generic 'error' on connection drop; payload is undefined.
      }
    })

    es.addEventListener('ended', () => {
      dispatch({ kind: 'status', text: 'ended' })
    })

    es.onerror = () => {
      dispatch({ kind: 'status', text: 'reconnecting…' })
    }

    return () => {
      es.close()
    }
  }, [])

  return state
}
