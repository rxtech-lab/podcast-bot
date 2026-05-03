import type { Session, TranscriptDTO } from './types'

export async function loadHistory(): Promise<TranscriptDTO[]> {
  const resp = await fetch('/api/transcript')
  if (!resp.ok) return []
  return (await resp.json()) as TranscriptDTO[]
}

export async function loadTopics(): Promise<Session[]> {
  const resp = await fetch('/api/topics')
  if (!resp.ok) return []
  return (await resp.json()) as Session[]
}

export async function sendMessage(text: string): Promise<void> {
  await fetch('/api/messages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
}
