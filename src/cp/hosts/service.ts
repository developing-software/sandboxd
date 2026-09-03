// Host admin use cases behind the HTTP API: list, approve, revoke.
import type { Store } from "../store.ts";
import type { HostPresence } from "./hub.ts";
import { badRequest, conflict, notFound } from "../../shared/errors.ts";
import { logger } from "../../shared/log.ts";

const log = logger("hosts");

export class HostService {
  constructor(private store: Store, private hub: HostPresence) {}

  list() {
    return this.store.listHosts().map((h) => ({
      id: h.id, name: h.name, status: h.status,
      approve_code: h.status === "pending" ? h.approve_code : undefined,
      online: this.hub.isOnline(h.id), capacity: this.hub.capacity(h.id),
      last_seen_at: h.last_seen_at, created_at: h.created_at,
    }));
  }

  approve(id: string, code: string | undefined) {
    const host = this.store.hostById(id);
    if (!host) throw notFound("host");
    if (host.status !== "pending") throw conflict(`host is ${host.status}`);
    if (!code || code.toUpperCase() !== host.approve_code) throw badRequest("approval code does not match");
    this.store.approveHost(host.id);
    log.info("host approved", { hostId: host.id, name: host.name });
    this.hub.notifyApproved(host.id);
  }

  revoke(id: string) {
    if (!this.store.hostById(id)) throw notFound("host");
    this.store.revokeHost(id);
  }
}
