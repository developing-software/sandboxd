#!/usr/bin/env bun
// The whole stack in one terminal: the API, the UI and a worker on this machine, each
// line prefixed with who said it. Ctrl-C stops all three.
import { boxed } from './utils/box'
import { paint } from './utils/color'

const root = `${import.meta.dir}/..`
const decoder = new TextDecoder()
const children = new Set<ReturnType<typeof Bun.spawn>>()
type IO = 'pipe' | 'inherit' | 'ignore'

const apiport = Number(process.env.SANDBOXD_PORT ?? 8080)
const uiport = Number(process.env.SANDBOXD_UI_PORT ?? 8081)
const ports = [apiport, uiport]

const env = {
  ...process.env,
  SANDBOXD_PORT: String(apiport),
  SANDBOXD_UI_PORT: String(uiport),
  SANDBOXD_DB: process.env.SANDBOXD_DB ?? `${root}/.data/cp.db`,
  SANDBOXD_API_URL: process.env.SANDBOXD_API_URL ?? `http://localhost:${apiport}`,
  SANDBOXD_URL: process.env.SANDBOXD_URL ?? `ws://localhost:${apiport}`,
  SANDBOXD_WORKER_CONFIG: process.env.SANDBOXD_WORKER_CONFIG ?? `${root}/.data/host.json`,
  SANDBOXD_LLM_BASE_URL: process.env.SANDBOXD_LLM_BASE_URL ?? 'https://llm.developing.company',
}

// The file, not the package script: `bun run <script>` forks a grandchild, and `stop`
// only ever sees the wrapper — the orphan then keeps its port for good.
// The worker is not hot-reloaded: a reload would orphan every PTY and container it owns.
const servers = [
  {
    name: 'api',
    cwd: `${root}/apps/api`,
    cmd: ['bun', 'run', '--hot', 'src/main.ts'],
    color: paint.cyan,
  },
  {
    name: 'ui',
    cwd: `${root}/apps/ui`,
    cmd: ['bun', 'run', '--hot', 'src/main.ts'],
    color: paint.yellow,
  },
  {
    name: 'worker',
    cwd: `${root}/apps/worker`,
    cmd: ['bun', 'run', 'src/main.ts'],
    color: paint.magenta,
  },
] as const

function spawn(cmd: readonly string[], cwd: string, out: IO = 'inherit', err: IO = out) {
  const child = Bun.spawn([...cmd], { cwd, env, stdin: 'inherit', stdout: out, stderr: err })
  children.add(child)
  child.exited.finally(() => children.delete(child))
  return child
}

async function pipe(stream: ReadableStream<Uint8Array>, tag: string, ready?: () => void) {
  for await (const chunk of stream) {
    const text = decoder.decode(chunk)
    for (const line of text.split('\n')) {
      if (line) process.stdout.write(`${tag}${line}\n`)
    }
    if (text.includes('listening')) ready?.()
  }
}

// SIGTERM is advisory once a child installs its own handler, so escalate. One child
// that ignores it would otherwise keep `stop` pending and leak the whole stack.
async function stop() {
  await Promise.all(
    [...children].map(async (child) => {
      child.kill()
      const timer = setTimeout(() => child.kill('SIGKILL'), 3000)
      await child.exited
      clearTimeout(timer)
    }),
  )
}

// A second Ctrl-C bails out instead of queueing another stop behind a stuck one.
let stopping = false
async function bye() {
  if (stopping) {
    console.log(
      paint.red('\nForced exit — surviving children keep their ports and sandboxes.'),
    )
    const pids = [...children].map((child) => child.pid)
    if (pids.length > 0) console.log(`  kill -9 ${pids.join(' ')}`)
    console.log(`  lsof -ti ${ports.map((port) => `:${port}`).join(' ')} | xargs -r kill -9`)
    process.exit(1)
  }
  stopping = true
  console.log('\nStopping... press Ctrl-C again to force.')
  await stop()
  process.exit(0)
}

process.on('SIGINT', bye)
process.on('SIGTERM', bye)

const width = Math.max(...servers.map((server) => server.name.length))

console.log(
  boxed([
    'STACK RUNNING',
    '',
    `ui      http://localhost:${uiport}`,
    `api     http://localhost:${apiport}   docs at /doc`,
    'worker  this machine; approve it in the UI with the printed code',
  ]),
)

function start(server: (typeof servers)[number], ready?: () => void) {
  const child = spawn(server.cmd, server.cwd, 'pipe', 'pipe')
  const tag = `${server.color(server.name.padEnd(width))} │ `
  for (const stream of [child.stdout, child.stderr]) {
    if (stream instanceof ReadableStream) void pipe(stream, tag, ready)
  }
  return child
}

// The API first: the UI forwards to it and the worker dials it, and a page already open
// on the UI polls at once. Both cope with a restart, but the first start should be quiet.
// Under --hot a module that throws at startup stays alive waiting for a save, so the
// child never exits — the timeout is what turns that into a failure here.
const [api, ...rest] = servers
const first = await new Promise<ReturnType<typeof spawn>>((resolve, reject) => {
  const child = start(api, () => resolve(child))
  void child.exited.then((code) =>
    reject(new Error(`api exited with code ${code} before listening`)),
  )
  setTimeout(
    () =>
      reject(
        new Error(
          'api did not start within 15 s — see its output above (an old .data/cp.db? delete it)',
        ),
      ),
    15_000,
  )
}).catch(async (e: Error) => {
  console.log(paint.red(`\n${e.message}`))
  await stop()
  process.exit(1)
})

const exit = await Promise.race([first.exited, ...rest.map((server) => start(server).exited)])

await stop()
process.exit(exit)
