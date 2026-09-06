// The fleet is not in the published SDK: these types are generated here from
// api/admin.yaml, and may change when the operator surface does (decision 27).
import type { HostView } from '../admin/types.gen'
import { Client } from './client'
import { $, Shell, esc } from './shell'

Shell.init()
const refresh = Shell.poll(async () => render(await Client.get<HostView[]>('/hosts')))

let last = ''
function render(hosts: HostView[]) {
  const html =
    hosts.length > 0
      ? hosts.map((h) => card(h)).join('')
      : '<p class="muted">no hosts yet — run <code>go run ./cmd/sandboxd-worker</code></p>'
  // A re-render would clobber a code being typed: skip when nothing changed, and carry
  // the typed codes over when something did.
  if (html === last) return
  const typed = new Map<string, string>()
  for (const i of inputs()) typed.set(i.form!.dataset.approve!, i.value)
  last = html
  $('hosts').innerHTML = html
  for (const i of inputs()) i.value = typed.get(i.form!.dataset.approve!) ?? ''
}
const inputs = () => document.querySelectorAll<HTMLInputElement>('#hosts input[name=code]')

const card = (h: HostView) =>
  h.status === 'pending'
    ? `<div class="card pending">
        <b>${esc(h.name)}</b><span class="pill warn">waiting for approval</span>
        <div class="id">${esc(h.id)}</div>
        <form class="row actions" data-approve="${esc(h.id)}">
          <input name="code" placeholder="code printed by the worker" autocomplete="off">
          <button>approve</button>
        </form>
      </div>`
    : `<div class="card">
        <b>${esc(h.name)}</b>
        <span class="pill ${h.online ? 'on' : 'off'}">${h.online ? 'online' : 'offline'}</span>
        ${h.capacity ? `<span class="pill">${h.capacity.running}/${h.capacity.max} sandboxes</span>` : ''}
        ${h.status === 'revoked' ? '<span class="pill off">revoked</span>' : ''}
        ${(h.tags ?? []).map((t) => `<span class="pill">${esc(t)}</span>`).join('')}
        <div class="id">${esc(h.id)}</div>
        ${h.status === 'approved' ? `<div class="actions"><button class="danger" data-revoke="${esc(h.id)}">revoke</button></div>` : ''}
      </div>`

$('hosts').addEventListener('submit', async (e) => {
  e.preventDefault()
  const form = e.target as HTMLFormElement
  const input = form.elements.namedItem('code') as HTMLInputElement
  const code = input.value.trim().toUpperCase()
  if (!code) return input.focus()
  await Client.post(`/hosts/${form.dataset.approve}/approve`, { code }).then(
    refresh,
    (err) => {
      alert((err as Error).message)
      input.select()
    },
  )
})

$('hosts').addEventListener('click', async (e) => {
  const id = (e.target as HTMLElement).dataset.revoke
  if (!id || !confirm('Revoke this host? Its sandboxes will be lost.')) return
  await Client.post(`/hosts/${id}/revoke`).catch((err) => alert((err as Error).message))
  await refresh()
})
