export type Role =
  | 'host'
  | 'affirmative'
  | 'negative'
  | 'judge'
  | 'viewer'
  | 'user'

export interface SourceRef {
  title: string
  url: string
  snippet?: string
}

export interface ChatLine {
  speaker: string
  role: Role | string
  side: string
  text: string
  sources?: SourceRef[]
  judgementComment?: string
}

export interface TranscriptDTO {
  speaker: string
  role: Role | string
  side: string
  text: string
  at: string
  sources?: SourceRef[]
  judgement_comment?: string
}

export interface TranscriptEvent {
  speaker: string
  role: Role | string
  side: string
  text: string
  channel_id?: string
  done: boolean
  sources?: SourceRef[]
  judgement_comment?: string
}

export interface TickEvent {
  channel_id?: string
  elapsed_ms: number
  remaining_ms: number
}

export interface PhaseEvent {
  channel_id?: string
  phase: string
  // Server-stamped human-readable label, content-type aware (e.g. "問答"
  // for puzzle Q&A, "自由辯論" for debate free-speech). Frontend should
  // display this directly rather than mapping `phase` itself.
  label?: string
  type?: string
}

export interface StatusEvent {
  channel_id?: string
  text: string
}

export interface ErrorEvent {
  channel_id?: string
  text: string
}

export interface EndedEvent {
  channel_id?: string
  transcript_path: string
  audio_path: string
}

export type SessionStatus = 'pending' | 'running' | 'done' | 'error'

// One debate within a channel's sequential queue.
export interface Session {
  id: string
  title: string
  status: SessionStatus
  transcript_path?: string
  audio_path?: string
}

// One TV-style channel: id + display number/title, plus the queue of debates
// assigned to it. off_air = true when the channel is defined in channels.json
// but no debates target it (no encoder is spawned in this case).
export interface ChannelInfo {
  id: string
  number: number
  title: string
  off_air: boolean
  debates: Session[]
  current_debate_id?: string
}

// Server response shape for GET /api/topics.
export interface TopicsResponse {
  channels: ChannelInfo[]
}

export interface TopicEvent {
  channel_id?: string
  id: string
  title: string
  type?: string
  index: number
  total: number
}
