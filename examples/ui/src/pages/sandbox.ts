// One sandbox: its facts, its terminal, a preview and the way to end it. The page polls
// the sandbox and attaches the terminal the moment it is running.
import type { Link, SandboxView } from '@sandboxd/sdk'
import { Client } from './client'
import { Format } from './format'
import { $, Shell, esc } from './shell'
import { Term } from './term'

Shell.init()
const id = location.pathname.split('/').at(-1)!
const term = new Term($('term'))
$('id').textContent = id
document.title = `${id} · sandboxd`
// The create page passes the preset's preview port along; without one, a common default.
$<HTMLInputElement>('port').value = new URLSearchParams(location.search).get('port') ?? '3000'

const refresh = Shell.poll(async () => {
  const s = await Client.get<SandboxView>(`/sandboxes/${id}`)
  render(s)
  onStatus(s)
})

let shown = ''
function render(s: SandboxView) {
  const facts = JSON.stringify(s)
  if (facts === shown) return
  shown = facts
  $('status').className = `pill ${Format.tone(s)}`
  $('status').textContent = Format.status(s)
  $<HTMLButtonElement>('end').disabled = s.status === 'ended'
  $<HTMLButtonElement>('preview').disabled = s.status !== 'running'
  const host = s.host_id
    ? `${s.host_id}${s.host_online === null ? '' : s.host_online ? ' (online)' : ' (offline)'}`
    : ''
  const env = Object.entries(s.env)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
  $('facts').innerHTML = [
    ['image', s.image],
    ['cmd', (s.cmd ?? []).join(' ')],
    ['host', host],
    ['tags', (s.tags ?? []).join(' ')],
    ['idle timeout', `${s.idle_timeout_s}s`],
    ['created', Format.when(s.created_at)],
    ['started', Format.when(s.started_at)],
    ['ended', Format.when(s.ended_at)],
    ['detail', s.ended_detail ?? ''],
    ['env', env],
  ]
    .filter(([, v]) => v)
    .map(([k, v]) => `<dt>${k}</dt><dd style="white-space:pre-wrap">${esc(v)}</dd>`)
    .join('')
}

// Every status change is a line in the terminal; the one into running opens it.
let last = ''
function onStatus(s: SandboxView) {
  const now = Format.status(s)
  if (now === last) return
  last = now
  term.note(now, s.status === 'ended' ? 'bad' : 'muted')
  if (s.status === 'running') attach().catch((err) => term.note((err as Error).message, 'bad'))
}

// Both capabilities are one Link: a URL with the credential already in it, good for a
// while. The page's only move is to open it, which is why there is no token to hold.
async function attach() {
  const { url } = await Client.post<Link>(`/sandboxes/${id}/terminal`)
  term.connect(url)
}

$('preview').addEventListener('click', async () => {
  const port = Number($<HTMLInputElement>('port').value)
  await Client.post<Link>(`/sandboxes/${id}/preview`, { port }).then(
    ({ url }) => window.open(url, '_blank'),
    (err) => alert((err as Error).message),
  )
})

$('end').addEventListener('click', async () => {
  await Client.del(`/sandboxes/${id}`).catch((err) => alert((err as Error).message))
  await refresh()
})
