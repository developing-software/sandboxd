import type { Link, SandboxView } from '@sandboxd/sdk'
import { Preview } from '../preview'
import type { PresetInfo } from '../presets/types'
import { Client } from './client'
import { Format } from './format'
import { $, Shell, esc } from './shell'

Shell.init()
let presets: PresetInfo[] = []
const refresh = Shell.poll(async () => render(await Client.get<SandboxView[]>('/sandboxes')))
// The link on a card needs the presets, so redraw once they land rather than wait a tick.
async function load() {
  presets = await Client.get<PresetInfo[]>('/presets')
  await refresh()
}
load().catch((err: Error) => Shell.fail(err.message))

function render(list: SandboxView[]) {
  $('sandboxes').innerHTML =
    list.length > 0 ? list.map((s) => card(s)).join('') : '<p class="muted">none yet</p>'
}

// A running sandbox whose preset declared a preview port is linked from here too: the
// terminal is not the only reason to come back to one, and a `vscode` sandbox is opened
// far more often than it is watched.
function card(s: SandboxView) {
  const p = s.status === 'running' ? Preview.detect(presets, s) : null
  const open = p
    ? `<button data-id="${esc(s.id)}" data-port="${p.port}">open ${esc(p.preset)}</button>`
    : ''
  return `<div class="card link">
    <a href="/sandboxes/${esc(s.id)}">
      <b>${esc(s.id)}</b><span class="pill ${Format.tone(s)}">${esc(Format.status(s))}</span>
      <div class="id">${[(s.cmd ?? []).join(' '), s.image, ...(s.tags ?? [])]
        .filter(Boolean)
        .map((v) => esc(v))
        .join(' · ')}</div>
      ${s.ended_detail ? `<div class="id">${esc(s.ended_detail)}</div>` : ''}
      <div class="id">${Format.when(s.created_at)}</div>
    </a>
    ${open}
  </div>`
}

$('sandboxes').addEventListener('click', (e) => {
  const el = (e.target as HTMLElement).closest<HTMLElement>('[data-port]')
  if (!el) return
  const { id, port } = el.dataset
  Shell.openLink(() =>
    Client.post<Link>(`/sandboxes/${id}/preview`, { port: Number(port) }),
  ).catch((err: Error) => alert(err.message))
})
