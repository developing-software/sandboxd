// The create form. Presets are data in this app (presets/<name>/preset.yaml), so the form
// is rendered from GET /api/presets; the API only ever sees the expanded result.
import type { SandboxView } from '@sandboxd/sdk'
import type { FieldSpec, PresetInfo } from '../presets/types'
import { Client } from './client'
import { $, Shell, esc } from './shell'

Shell.init()
const presets = new Map<string, PresetInfo>()
const select = $<HTMLSelectElement>('preset')
const chosen = () => presets.get(select.value)!

async function load() {
  const list = await Client.get<PresetInfo[]>('/presets')
  for (const p of list) presets.set(p.name, p)
  select.innerHTML = list
    .map(
      (p) => `<option value="${esc(p.name)}">${esc(p.name)} — ${esc(p.description)}</option>`,
    )
    .join('')
  select.value = presets.has('coding-agent') ? 'coding-agent' : (list[0]?.name ?? '')
  renderFields()
}
load().catch((err) => Shell.fail((err as Error).message))

select.addEventListener('change', renderFields)

function renderFields() {
  const p = chosen()
  $('presetDesc').textContent =
    (p.image ? `image ${p.image}` : 'image: from the caller, else SANDBOXD_DEFAULT_IMAGE') +
    (p.idle_timeout_s ? ` · idle ${p.idle_timeout_s / 60} min` : '')
  $('fields').innerHTML = p.fields.map((f) => field(f)).join('')
}

function field(f: FieldSpec) {
  const id = `f-${f.name.replaceAll('.', '-')}`
  const hint = f.description ? ` <span class="muted">(${esc(f.description)})</span>` : ''
  const label = `<label for="${id}">${esc(f.name)}${f.required ? ' *' : ''}${hint}</label>`
  const attrs = `id="${id}" data-path="${esc(f.name)}" data-type="${f.type}"`
  if (f.type === 'enum') {
    const none = f.default ? `${esc(f.default)} (default)` : 'default'
    const values = f
      .values!.map((v) => `<option value="${esc(v)}">${esc(v)}</option>`)
      .join('')
    return `${label}<select ${attrs}><option value="">${none}</option>${values}</select>`
  }
  if (f.type === 'bool')
    return `<div class="check"><input type="checkbox" ${attrs} data-default="${!!f.default}"${f.default ? ' checked' : ''}><label for="${id}">${esc(f.name)}${hint}</label></div>`
  if (f.multiline) return `${label}<textarea ${attrs}></textarea>`
  const type = f.secret ? 'password' : f.type === 'int' ? 'number' : 'text'
  const min = f.min === undefined ? '' : ` min="${f.min}"`
  const max = f.max === undefined ? '' : ` max="${f.max}"`
  const placeholder = f.default === undefined ? '' : esc(f.default)
  return `${label}<input type="${type}" ${attrs}${min}${max} placeholder="${placeholder}">`
}

/** Non-empty field inputs → nested body paths (`llm.base_url` → body.llm.base_url), typed per the schema. */
function collect(body: Record<string, unknown>) {
  for (const el of $('fields').querySelectorAll<HTMLInputElement>('[data-path]')) {
    const { path, type } = el.dataset as { path: string; type: FieldSpec['type'] }
    let v: unknown
    if (type === 'bool') {
      if (el.checked === (el.dataset.default === 'true')) continue
      v = el.checked
    } else {
      v = el.value.trim()
      if (v === '') continue
      if (type === 'int') v = Number(v)
    }
    const parts = path.split('.')
    let cur = body
    for (const k of parts.slice(0, -1)) cur = (cur[k] ??= {}) as Record<string, unknown>
    cur[parts.at(-1)!] = v
  }
}

/** The port the sandbox page should offer to preview: the preset's, or the field that names it. */
function previewPort(body: Record<string, unknown>) {
  const preview = chosen().preview
  if (!preview) return null
  if (!preview.port_field) return preview.port
  let cur: unknown = body
  for (const k of preview.port_field.split('.')) cur = (cur as Record<string, unknown>)?.[k]
  return typeof cur === 'number' ? cur : preview.port
}

function parseEnv(text: string) {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t || t.startsWith('#')) continue
    const i = t.indexOf('=')
    if (i < 1) throw new Error(`env: expected KEY=value, got "${t}"`)
    out[t.slice(0, i).trim()] = t.slice(i + 1)
  }
  return out
}

/** `bash -l`, or a JSON array for arguments with spaces. */
function parseCmd(text: string): string[] | undefined {
  const t = text.trim()
  if (!t) return undefined
  return t.startsWith('[') ? (JSON.parse(t) as string[]) : t.split(/\s+/)
}

const value = (id: string) => $<HTMLInputElement>(id).value

$('form').addEventListener('submit', async (e) => {
  e.preventDefault()
  $('createMsg').textContent = ''
  try {
    const tags = value('tags')
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean)
    const body: Record<string, unknown> = {
      preset: select.value,
      image: value('image').trim() || undefined,
      cmd: parseCmd(value('cmd')),
      tags: tags.length > 0 ? tags : undefined,
      env: parseEnv(value('env')),
      secret_env: parseEnv(value('secretEnv')),
    }
    collect(body)
    const port = previewPort(body)
    const s = await Client.post<SandboxView>('/sandboxes', body)
    location.assign(`/sandboxes/${s.id}${port ? `?port=${port}` : ''}`)
  } catch (err) {
    $('createMsg').textContent = (err as Error).message
  }
})
