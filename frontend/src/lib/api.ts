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
  if (!resp.ok) return { channels: [] }
  return (await resp.json()) as TopicsResponse
}

// sendMessage posts to the active channel's orchestrator. The viewer's
// username travels via the `debate-bot-username` cookie that GET /api/me
// installs on first load — `credentials: 'same-origin'` is explicit so the
// cookie is included even when the global fetch default is `omit` (older
// browsers, some webview embedders). Throws on any non-2xx so callers can
// surface the failure (otherwise a 503 "no active debate" would silently
// drop the user's message and the chat would mysteriously stay empty).
export async function sendMessage(text: string, channelId?: string): Promise<void> {
  const resp = await fetch(`/api/messages${channelQuery(channelId)}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  })
  if (!resp.ok) {
    const body = await resp.text().catch(() => '')
    throw new Error(`send failed (${resp.status}): ${body || resp.statusText}`)
  }
}

export interface Me {
  username: string
}

// loadMe fetches (and on first call, provisions) the viewer's persistent
// username. The server replies with Set-Cookie when issuing a fresh handle.
export async function loadMe(): Promise<Me> {
  const resp = await fetch('/api/me', { credentials: 'same-origin' })
  if (!resp.ok) return { username: '' }
  return (await resp.json()) as Me
}

// updateMe changes the viewer's username. Server sanitises the input and
// resets to a random handle when the value is empty / invalid.
export async function updateMe(username: string): Promise<Me> {
  const resp = await fetch('/api/me', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username }),
  })
  if (!resp.ok) return { username }
  return (await resp.json()) as Me
}
