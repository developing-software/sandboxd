// The service catalog: presets/services.yaml, an ordinary compose file whose
// services a session can pick by name. Loaded once at boot through the same
// translator that handles a caller's `compose` field.
import { existsSync, readFileSync } from "node:fs";
import type { ServiceDecl } from "../store.ts";
import { parseCompose } from "../compose.ts";
import type { CatalogEntry, ServiceCatalog, ValidatedServices } from "../services.ts";

/** What GET /services shows: enough for a UI to offer the entry and for a caller to know what it gets. */
export interface CatalogView {
  name: string; image: string; cmd: string[] | null; ready: ServiceDecl["ready"]; sandbox_env: Record<string, string>;
}

export class ComposeCatalog implements ServiceCatalog {
  private entries = new Map<string, CatalogEntry>();

  constructor(services?: ValidatedServices) {
    for (const decl of services?.decls ?? []) {
      this.entries.set(decl.name, { decl, secret_env: services!.secrets[decl.name] ?? {}, sandbox_env: services!.sandbox_env[decl.name] ?? {} });
    }
  }

  get names(): string[] { return [...this.entries.keys()]; }
  get(name: string): CatalogEntry | undefined { return this.entries.get(name); }
  list(): CatalogView[] {
    return [...this.entries.values()].map(({ decl, sandbox_env }) => ({ name: decl.name, image: decl.image, cmd: decl.cmd, ready: decl.ready, sandbox_env }));
  }
}

/** Empty when the file does not exist; a malformed file is a boot error naming it. */
export function loadCatalog(file: string): ComposeCatalog {
  if (!existsSync(file)) return new ComposeCatalog();
  try {
    return new ComposeCatalog(parseCompose(readFileSync(file, "utf8"), "services"));
  } catch (e) {
    throw new Error(`${file}: ${(e as Error).message}`);
  }
}
