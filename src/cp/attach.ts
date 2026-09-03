// Browser <-> PTY bridge. Binary frames are raw PTY bytes both ways; the
// browser sends {"type":"resize"} as text; we send {"type":"closed"} as text.
import type { ServerWebSocket } from "bun";
import type { AttachClientMsg, AttachServerMsg } from "../protocol/messages.ts";
import type { PtyOpener } from "./hosts/hub.ts";
import type { PtyHandle } from "./hosts/conn.ts";
import type { Store } from "./store.ts";

export interface AttachData {
  kind: "attach";
  sid: string;
  cols: number;
  rows: number;
  pty: PtyHandle | null;
}
type WS = ServerWebSocket<AttachData>;

const closed = (reason: string) => JSON.stringify({ type: "closed", reason } satisfies AttachServerMsg);

export class AttachBridge {
  constructor(private store: Store, private hub: PtyOpener) {}

  onOpen(ws: WS) {
    const s = this.store.session(ws.data.sid);
    if (!s || s.status !== "running" || !s.host_id) { ws.send(closed("session not running")); ws.close(); return; }
    const pty = this.hub.openPty(s.host_id, s.id, { cols: ws.data.cols, rows: ws.data.rows }, {
      onData: (d) => { ws.send(d); },
      onClose: () => { ws.data.pty = null; try { ws.send(closed("pty closed")); } catch {} ws.close(); },
    });
    if (!pty) { ws.send(closed("host offline")); ws.close(); return; }
    ws.data.pty = pty;
  }

  onMessage(ws: WS, msg: string | Buffer) {
    const pty = ws.data.pty;
    if (!pty) return;
    if (typeof msg === "string") {
      try {
        const m = JSON.parse(msg) as AttachClientMsg;
        if (m.type === "resize" && m.cols > 0 && m.rows > 0) pty.resize({ cols: m.cols | 0, rows: m.rows | 0 });
      } catch {}
      return;
    }
    pty.write(new Uint8Array(msg));
  }

  onClose(ws: WS) {
    ws.data.pty?.close();
    ws.data.pty = null;
  }
}
