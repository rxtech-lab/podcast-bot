// /api/config tells the SPA which mode the server was started in.
// "stream" → channel-tuner UI; "video" → upload-and-render UI.
export type ServerMode = 'stream' | 'video'

export interface ServerConfig {
  mode: ServerMode
  // authRequired: the server was started with a password. When true and
  // authed is false, the SPA shows the login screen before anything else.
  authRequired: boolean
  // authed: a valid auth cookie is already present (returning visitor /
  // page reload), so the login screen can be skipped.
  authed: boolean
}

export async function loadConfig(): Promise<ServerConfig> {
  try {
    const resp = await fetch('/api/config')
    if (!resp.ok) return { mode: 'stream', authRequired: false, authed: true }
    const body = (await resp.json()) as Partial<{
      mode: ServerMode
      auth_required: boolean
      authed: boolean
    }>
    return {
      mode: body.mode === 'video' ? 'video' : 'stream',
      authRequired: body.auth_required === true,
      authed: body.authed !== false,
    }
  } catch {
    return { mode: 'stream', authRequired: false, authed: true }
  }
}

// login submits the password. Resolves true when accepted (the server sets
// the auth cookie), false on an invalid password.
export async function login(password: string): Promise<boolean> {
  try {
    const resp = await fetch('/api/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    })
    return resp.ok
  } catch {
    return false
  }
}
