// /api/config tells the SPA which mode the server was started in.
// "stream" → channel-tuner UI; "video" → upload-and-render UI.
export type ServerMode = 'stream' | 'video'

export interface ServerConfig {
  mode: ServerMode
}

export async function loadConfig(): Promise<ServerConfig> {
  try {
    const resp = await fetch('/api/config')
    if (!resp.ok) return { mode: 'stream' }
    const body = (await resp.json()) as Partial<ServerConfig>
    return { mode: body.mode === 'video' ? 'video' : 'stream' }
  } catch {
    return { mode: 'stream' }
  }
}
