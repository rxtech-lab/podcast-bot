export type Role =
  | 'host'
  | 'affirmative'
  | 'negative'
  | 'judge'
  | 'viewer'
  | 'user'

export interface ChatLine {
  speaker: string
  role: Role | string
  side: string
  text: string
}

export interface TranscriptDTO extends ChatLine {
  at: string
}

export interface TranscriptEvent extends ChatLine {
  done: boolean
}

export interface TickEvent {
  elapsed_ms: number
  remaining_ms: number
}

export interface PhaseEvent {
  phase: string
}

export interface StatusEvent {
  text: string
}

export interface ErrorEvent {
  text: string
}

export interface EndedEvent {
  transcript_path: string
  audio_path: string
}

export type SessionStatus = 'pending' | 'running' | 'done' | 'error'

export type Mode = 'sequential' | 'parallel'

export interface Session {
  id: string
  title: string
  status: SessionStatus
  transcript_path?: string
  audio_path?: string
}

// Server response shape for GET /api/topics. The mode determines whether the
// frontend scopes its URLs by channel id (parallel) or shares one stream
// across the queue (sequential).
export interface TopicsResponse {
  mode: Mode
  items: Session[]
}

export interface TopicEvent {
  channel_id?: string
  id: string
  title: string
  index: number
  total: number
}
