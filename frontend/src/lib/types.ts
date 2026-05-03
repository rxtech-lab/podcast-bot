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
