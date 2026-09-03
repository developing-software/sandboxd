// Reads presets/<name>/preset.yaml for every subdirectory of the presets dir,
// plus presets/services.yaml. Synchronous and boot-time only: a bad file stops
// the control plane with a message naming it.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { parsePresetDoc } from "./schema.ts";
import { presetFromDoc } from "./preset.ts";
import { ComposeCatalog, loadCatalog } from "./catalog.ts";
import type { Preset } from "./types.ts";

export const PRESET_FILE = "preset.yaml";
export const CATALOG_FILE = "services.yaml";

export interface LoadedPresets { dir: string; presets: Preset[]; catalog: ComposeCatalog }

export function loadPresetDir(dir: string): LoadedPresets {
  const abs = resolve(dir);
  if (!existsSync(abs) || !statSync(abs).isDirectory()) throw new Error(`presets dir not found: ${abs} (set DEVAGENTS_PRESETS_DIR)`);
  const catalog = loadCatalog(join(abs, CATALOG_FILE));
  const presets: Preset[] = [];
  for (const entry of readdirSync(abs, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    if (!entry.isDirectory()) continue;
    const file = join(abs, entry.name, PRESET_FILE);
    if (!existsSync(file)) continue;
    presets.push(loadPresetFile(entry.name, file, catalog));
  }
  if (!presets.length) throw new Error(`no presets found in ${abs} (expected <name>/${PRESET_FILE})`);
  return { dir: abs, presets, catalog };
}

export function loadPresetFile(name: string, file: string, catalog: ComposeCatalog): Preset {
  try {
    const doc: unknown = Bun.YAML.parse(readFileSync(file, "utf8"));
    return presetFromDoc(parsePresetDoc(name, doc), catalog);
  } catch (e) {
    throw new Error(`${file}: ${(e as Error).message}`);
  }
}
