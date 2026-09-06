import type { SandboxView } from '@sandboxd/sdk'
import { Client } from './client'
import { Format } from './format'
import { $, Shell, esc } from './shell'

Shell.init()
Shell.poll(async () => render(await Client.get<SandboxView[]>('/sandboxes')))

function render(list: SandboxView[]) {
  $('sandboxes').innerHTML =
    list.length > 0 ? list.map((s) => card(s)).join('') : '<p class="muted">none yet</p>'
}

const card = (s: SandboxView) =>
  `<a class="card" href="/sandboxes/${esc(s.id)}">
    <b>${esc(s.id)}</b><span class="pill ${Format.tone(s)}">${esc(Format.status(s))}</span>
    <div class="id">${[(s.cmd ?? []).join(' '), s.image, ...(s.tags ?? [])]
      .filter(Boolean)
      .map((v) => esc(v))
      .join(' · ')}</div>
    ${s.ended_detail ? `<div class="id">${esc(s.ended_detail)}</div>` : ''}
    <div class="id">${Format.when(s.created_at)}</div>
  </a>`
