import type { TranscriptDTO } from './types'

export async function loadHistory(): Promise<TranscriptDTO[]> {
  const resp = await fetch('/api/transcript')
  if (!resp.ok) return []
  return (await resp.json()) as TranscriptDTO[]
}

export async function sendMessage(text: string): Promise<void> {
  await fetch('/api/messages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
}
