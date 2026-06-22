import { useState } from 'react'
import { login } from '@/lib/config'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

// Login gates the app when the server was started with a password. On a
// successful POST /api/login the server sets the auth cookie; we then call
// onSuccess so the parent re-renders the real UI.
export function Login({ onSuccess }: { onSuccess: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!password || busy) return
    setBusy(true)
    setError('')
    const ok = await login(password)
    setBusy(false)
    if (ok) {
      onSuccess()
    } else {
      setError('Incorrect password')
      setPassword('')
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background text-foreground font-sans">
      <form
        onSubmit={submit}
        className="w-full max-w-sm rounded-xl border border-border bg-card p-6 shadow-lg"
      >
        <h1 className="mb-1 text-lg font-semibold">debate-bot</h1>
        <p className="mb-4 text-sm text-muted-foreground">
          This server is password-protected. Enter the password to continue.
        </p>
        <Input
          type="password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Password"
          aria-label="Password"
          className="mb-3"
        />
        {error && <p className="mb-3 text-sm text-destructive">{error}</p>}
        <Button type="submit" disabled={busy || !password} className="w-full">
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </form>
    </div>
  )
}
