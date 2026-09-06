// xterm.js on the attach WebSocket: bytes both ways, JSON for resize and for the reason
// the control plane sends before it hangs up.
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

const enc = new TextEncoder()

export class Term {
  private term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    scrollback: 10000,
    scrollSensitivity: 2,
    theme: { background: '#000000' },
  })
  private fit = new FitAddon()
  private ws: WebSocket | null = null

  constructor(el: HTMLElement) {
    this.term.loadAddon(this.fit)
    this.term.open(el)
    this.fit.fit()
    // Refit at most once per frame and only when the geometry actually changed: every fit
    // that changes rows/cols becomes a SIGWINCH in the sandbox, and TUI agents redraw
    // their whole frame on each one, which yanks the scroll.
    let queued = false
    new ResizeObserver(() => {
      if (queued) return
      queued = true
      requestAnimationFrame(() => {
        queued = false
        const d = this.fit.proposeDimensions()
        if (d && (d.cols !== this.term.cols || d.rows !== this.term.rows)) this.fit.fit()
      })
    }).observe(el)
    this.term.onData((d) => this.send(enc.encode(d)))
    this.term.onBinary((d) => this.send(Uint8Array.from(d, (c) => c.codePointAt(0)!)))
    this.term.onResize(({ cols, rows }) =>
      this.send(JSON.stringify({ type: 'resize', cols, rows })),
    )
  }

  /** A line the terminal itself says, apart from what the sandbox prints. */
  note(text: string, tone: 'muted' | 'bad' = 'muted') {
    this.term.write(`\r\n[${tone === 'bad' ? '31' : '90'}m[${text}][0m\r\n`)
  }

  /** Open the socket the attach token points at. A previous one is closed quietly. */
  connect(wssUrl: string) {
    this.ws?.close()
    this.term.reset()
    const ws = new WebSocket(`${wssUrl}&cols=${this.term.cols}&rows=${this.term.rows}`)
    ws.binaryType = 'arraybuffer'
    ws.addEventListener('message', (ev: MessageEvent<string | ArrayBuffer>) => {
      if (typeof ev.data !== 'string') return this.term.write(new Uint8Array(ev.data))
      const m = JSON.parse(ev.data) as { type: string; reason: string }
      this.note(`${m.type}: ${m.reason}`, 'bad')
    })
    // Only the current socket's close is news; a replaced one closes because we said so.
    ws.addEventListener('close', () => {
      if (this.ws === ws) this.note('disconnected')
    })
    this.ws = ws
    this.term.focus()
  }

  private send(data: string | Uint8Array) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(data)
  }
}
