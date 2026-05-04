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
  channel_id?: string
  done: boolean
}

export interface TickEvent {
  channel_id?: string
  elapsed_ms: number
  remaining_ms: number
}

export interface PhaseEvent {
  channel_id?: string
  phase: string
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
  index: number
  total: number
}
