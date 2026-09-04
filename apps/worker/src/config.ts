import { mkdirSync, existsSync, readFileSync, writeFileSync, chmodSync } from 'node:fs'
import { hostname, homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { Id } from '@sandboxd/core/ids'

export interface WorkerConfig {
  cpUrl: string // ws(s)://cp.example.com
  name: string
  maxSessions: number
  dockerSock: string
  entry: string[] // command exec'd in the PTY inside the sandbox
  secret: string // self-generated, never leaves this machine
  fingerprint: string // sha256(secret) hex; what the CP sees
  joinToken: string | null // SANDBOXD_JOIN_TOKEN; sent in the hello, approves without the code
}

interface HostFile {
  secret: string
  name?: string
}

export function loadConfig(env = process.env): WorkerConfig {
  const cpUrl = env.SANDBOXD_URL ?? 'ws://localhost:8080'
  if (!cpUrl) throw new Error('SANDBOXD_URL is required (e.g. ws://localhost:8080)')
  const path =
    env.SANDBOXD_WORKER_CONFIG ?? join(homedir(), '.config', 'sandboxd', 'host.json')

  let file: HostFile
  if (existsSync(path)) {
    file = JSON.parse(readFileSync(path, 'utf8'))
  } else {
    file = { secret: Id.random(32) }
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
    writeFileSync(path, JSON.stringify(file, null, 2))
    chmodSync(path, 0o600)
  }

  const entryRaw = env.SANDBOXD_WORKER_ENTRY ?? '/usr/local/bin/sandboxd-entry'
  const entry = entryRaw.trim().startsWith('[')
    ? (JSON.parse(entryRaw) as string[])
    : entryRaw.split(/\s+/)

  return {
    cpUrl: cpUrl.replace(/\/+$/, ''),
    name: env.SANDBOXD_WORKER_NAME ?? file.name ?? hostname(),
    maxSessions: Number(env.SANDBOXD_WORKER_MAX_SESSIONS ?? 4),
    dockerSock: env.DOCKER_SOCK ?? '/var/run/docker.sock',
    entry,
    secret: file.secret,
    fingerprint: new Bun.CryptoHasher('sha256').update(file.secret).digest('hex'),
    joinToken: env.SANDBOXD_JOIN_TOKEN || null,
  }
}
