import { expect, test } from 'bun:test'
import { Database } from 'bun:sqlite'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store'

test('a db from another schema version is wiped at boot, not refused', () => {
  const dir = mkdtempSync(join(tmpdir(), 'sandboxd-store-'))
  const path = join(dir, 'cp.db')
  try {
    // An older control plane: a sessions table with a column this version does not have.
    const old = new Database(path, { create: true })
    old.exec(`
      CREATE TABLE hosts (id TEXT PRIMARY KEY);
      CREATE TABLE sessions (id TEXT PRIMARY KEY, services TEXT NOT NULL);
      INSERT INTO hosts VALUES ('h1');
      INSERT INTO sessions VALUES ('s_1', '[]');
      PRAGMA user_version = 4;
    `)
    old.close()

    const store = new Store(path)
    expect(store.listHosts()).toEqual([])
    expect(store.listSessions()).toEqual([])
    expect(store.db.query<{ user_version: number }, []>('PRAGMA user_version').get()).toEqual({
      user_version: 5,
    })
    const columns = store.db
      .query<{ name: string }, []>('PRAGMA table_info(sessions)')
      .all()
      .map((c) => c.name)
    expect(columns).not.toContain('services')
    expect(columns).toContain('idle_timeout_s')

    // The same version reopens without touching anything.
    store.insertPendingHost({
      id: 'h2',
      name: 'box',
      fingerprint: 'fp',
      approve_code: 'AAAA-AA',
      max_sessions: 1,
    })
    store.db.close()
    expect(new Store(path).listHosts().map((h) => h.id)).toEqual(['h2'])
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
