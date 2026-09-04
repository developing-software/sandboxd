import { expect, test } from 'bun:test'
import { Compose } from '../src/compose'

const sample = `
x-pg: &pg
  image: postgres:16
  environment:
    POSTGRES_PASSWORD: pw
    POSTGRES_PORT: 5432
    UNSET: null
services:
  db:
    <<: *pg
    ports: ["127.0.0.1:5433:5432"]
    healthcheck: { test: ["CMD", "pg_isready"], interval: 5s }
    depends_on: [cache]
    volumes: ["./pgdata:/var/lib/postgresql/data"]
    restart: unless-stopped
  cache:
    image: redis:7
    command: redis-server --appendonly yes --requirepass 'a b'
    environment: [ "A=1", "B=\${B:-two}", "C" ]
  app:
    build: .
    depends_on:
      db: { condition: service_healthy }
volumes: { pgdata: {} }
`

test('compose: anchors and merge keys, build-only app skipped, dependencies first, laptop keys ignored', () => {
  const r = Compose.parse(sample)
  expect(r.decls.map((d) => d.name)).toEqual(['cache', 'db'])
  const [cache, db] = r.decls
  expect(db).toEqual({
    name: 'db',
    image: 'postgres:16',
    env: { POSTGRES_PASSWORD: 'pw', POSTGRES_PORT: '5432' },
    cmd: null,
    ready: { port: 5432, timeout_s: 60 },
  })
  expect(cache).toEqual({
    name: 'cache',
    image: 'redis:7',
    env: { A: '1', B: 'two', C: '' },
    cmd: ['redis-server', '--appendonly', 'yes', '--requirepass', 'a b'],
    ready: null,
  })
  expect(r.secrets).toEqual({})
  expect(r.sandbox_env).toEqual({})
  expect(Compose.parse(Bun.YAML.parse(sample))).toEqual(r) // object input == text input
})

test('compose: readiness port from x-sandboxd, expose, or ports (short, long, udp ignored)', () => {
  const ready = (svc: Record<string, unknown>) =>
    Compose.parse({ services: { s: { image: 'i', ...svc } } }).decls[0]!.ready
  expect(ready({ 'x-sandboxd': { ready: { port: 9, timeout_s: 5 } }, expose: ['1'] })).toEqual(
    { port: 9, timeout_s: 5 },
  )
  expect(ready({ expose: [6379, '6380/tcp'] })).toEqual({ port: 6379, timeout_s: 60 })
  expect(ready({ ports: ['8080:80'] })).toEqual({ port: 80, timeout_s: 60 })
  expect(ready({ ports: ['53/udp', '80'] })).toBeNull()
  expect(ready({ ports: [{ target: 5432, published: 5433 }] })).toEqual({
    port: 5432,
    timeout_s: 60,
  })
  expect(ready({ ports: [{ target: 53, protocol: 'udp' }] })).toBeNull()
  expect(ready({})).toBeNull()
  expect(() => ready({ expose: ['a-b'] })).toThrow(/cannot read a port/)
  expect(() => ready({ 'x-sandboxd': { ready: { port: 0 } } })).toThrow(/ready.port must be/)
})

test('compose: command forms, x-sandboxd secret_env / sandbox_env, interpolation', () => {
  const r = Compose.parse({
    services: {
      a: {
        image: 'i:${TAG:-1}',
        command: ['sh', '-c', 'echo $$HOME'],
        'x-sandboxd': { secret_env: { PW: 's' }, sandbox_env: { URL: 'u://a' } },
      },
      b: { image: 'j', command: '' },
    },
  })
  expect(r.decls[0]).toMatchObject({ image: 'i:1', cmd: ['sh', '-c', 'echo $HOME'] })
  expect(r.decls[1]!.cmd).toBeNull()
  expect(r.secrets).toEqual({ a: { PW: 's' } })
  expect(r.sandbox_env).toEqual({ a: { URL: 'u://a' } })
  expect(Compose.interpolate('$$x ${A} $B ${C-d} ${D:+alt} ${E:-}', 't')).toBe('$x   d  ')
  expect(() => Compose.interpolate('${X:?need X}', 't')).toThrow(/need X/)
  expect(() => Compose.interpolate('${X?}', 't')).toThrow(/variable X is required/)
  expect(() => Compose.interpolate('${1bad}', 't')).toThrow(/bad substitution/)
  expect(Compose.split(`a "b c" d\\ e 'f"g' ""`)).toEqual(['a', 'b c', 'd e', 'f"g', ''])
})

test('compose: refused keys, bad shapes, cycles, names, empty and invalid input', () => {
  const svc =
    (s: Record<string, unknown>, name = 's') =>
    () =>
      Compose.parse({ services: { [name]: { image: 'i', ...s } } })
  expect(svc({ privileged: true })).toThrow(/services.s.privileged is not supported$/)
  expect(svc({ entrypoint: ['x'] })).toThrow(/entrypoint is not supported/)
  expect(svc({ made_up: 1 })).toThrow(/made_up is not supported \(unknown key\)/)
  expect(svc({ 'x-sandboxd': { nope: 1 } })).toThrow(/x-sandboxd.nope is not supported/)
  expect(svc({ 'x-other': 1 })).not.toThrow()
  expect(svc({ environment: 'A=1' })).toThrow(/environment must be a map or a list/)
  expect(svc({ environment: { A: [1] } })).toThrow(/environment.A must be a scalar/)
  expect(svc({ environment: { 'bad-name': '1' } })).toThrow(/invalid variable name/)
  expect(svc({ environment: { TERM: 'x' } })).toThrow(/reserved/)
  expect(svc({ command: { a: 1 } })).toThrow(/command must be a string or a list/)
  expect(svc({ depends_on: 'db' })).toThrow(/depends_on must be a list or a map/)
  expect(svc({}, 'Bad')).toThrow(/name must match/)
  expect(svc({}, 'sandbox')).toThrow(/reserved/)
  expect(() => Compose.parse({ services: { s: {} } })).toThrow(/services.s.image is required/)
  expect(() => Compose.parse({ services: { s: 'x' } })).toThrow(/services.s must be a map/)
  expect(() => Compose.parse({ services: [] })).toThrow(/services must be a map/)
  expect(() => Compose.parse([])).toThrow(/must be a compose document/)
  expect(() => Compose.parse('services: [')).toThrow(/invalid YAML/)
  expect(() =>
    Compose.parse({
      services: { a: { image: 'i', depends_on: ['b'] }, b: { image: 'i', depends_on: ['a'] } },
    }),
  ).toThrow(/depends_on cycle: a -> b -> a/)
  expect(
    Compose.parse({
      services: {
        a: { image: 'i', depends_on: ['gone', 'b'] },
        b: { image: 'i' },
        gone: { build: '.' },
      },
    }).decls.map((d) => d.name),
  ).toEqual(['b', 'a'])
  for (const empty of [undefined, null, '', '   ', {}, { services: null }])
    expect(Compose.parse(empty).decls).toEqual([])
  expect(() => Compose.parse({ services: { a: { image: 'i' } } }, 'compose')).not.toThrow()
})
