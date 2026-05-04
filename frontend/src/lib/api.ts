import type { TopicsResponse, TranscriptDTO } from './types'

const channelQuery = (channelId?: string) =>
  channelId ? `?channel=${encodeURIComponent(channelId)}` : ''

export async function loadHistory(channelId?: string): Promise<TranscriptDTO[]> {
  const resp = await fetch(`/api/transcript${channelQuery(channelId)}`)
  if (!resp.ok) return []
  return (await resp.json()) as TranscriptDTO[]
}

export async function loadTopics(): Promise<TopicsResponse> {
  const resp = await fetch('/api/topics')
  if (!resp.ok) return { mode: 'sequential', items: [] }
  return (await resp.json()) as TopicsResponse
}

export async function sendMessage(text: string, channelId?: string): Promise<void> {
  await fetch(`/api/messages${channelQuery(channelId)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
}
