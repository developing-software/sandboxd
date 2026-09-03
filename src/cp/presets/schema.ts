// Structural validation of a parsed preset.yaml. Pure: no filesystem. Throws plain
// Errors (this runs at boot, not per request); the loader prefixes the file name.
import type { FieldSpec, FieldType, PresetInfo } from "./types.ts";
import type { ServiceRef } from "../services.ts";

export interface PresetDoc extends PresetInfo {
  /** Body paths that must be present for this preset to claim a request that names none. */
  claims: string[];
  serviceRefs: ServiceRef[];
}

const TOP_KEYS = new Set(["name", "description", "image", "cmd", "idle_timeout_s", "preview", "claims_when", "services", "fields"]);
const FIELD_KEYS = new Set(["type", "env", "required", "secret", "default", "values", "min", "max", "multiline", "description"]);
const TYPES: FieldType[] = ["string", "int", "enum", "url", "bool"];
const PATH_RE = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/;
const ENV_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** Set by the daemon on every PTY; a preset may not map a field onto them. */
const RESERVED_ENV = new Set(["TERM", "DEVAGENTS_SESSION_ID"]);
const MIN_IDLE_S = 60;

export function parsePresetDoc(name: string, doc: unknown): PresetDoc {
  if (!isObj(doc)) throw new Error("must be a YAML map");
  for (const k of Object.keys(doc)) if (!TOP_KEYS.has(k)) throw new Error(`unknown key "${k}"`);
  if (doc.name !== undefined && doc.name !== name) throw new Error(`name "${String(doc.name)}" does not match the directory "${name}"`);
  if (typeof doc.description !== "string" || !doc.description.trim()) throw new Error("description is required");

  let image: string | null;
  if (doc.image === undefined) image = `devagents-${name}:latest`;
  else if (doc.image === null) image = null;
  else if (typeof doc.image === "string" && doc.image.trim()) image = doc.image.trim();
  else throw new Error("image must be a non-empty string or null");

  let cmd: string[] | null = null;
  if (doc.cmd !== undefined && doc.cmd !== null) {
    if (!Array.isArray(doc.cmd) || !doc.cmd.length || !doc.cmd.every((c) => typeof c === "string")) throw new Error("cmd must be a non-empty list of strings");
    cmd = doc.cmd as string[];
  }

  let idle: number | null = null;
  if (doc.idle_timeout_s !== undefined && doc.idle_timeout_s !== null) {
    if (!Number.isInteger(doc.idle_timeout_s) || (doc.idle_timeout_s as number) < MIN_IDLE_S) throw new Error(`idle_timeout_s must be an integer >= ${MIN_IDLE_S}`);
    idle = doc.idle_timeout_s as number;
  }

  const fields = parseFields(doc.fields);
  const byName = new Map(fields.map((f) => [f.name, f]));

  let preview: PresetDoc["preview"] = null;
  if (doc.preview !== undefined && doc.preview !== null) {
    if (!isObj(doc.preview)) throw new Error("preview must be a map");
    const { port, port_field, ...rest } = doc.preview;
    if (Object.keys(rest).length) throw new Error(`preview: unknown key "${Object.keys(rest)[0]}"`);
    if (port !== undefined && port_field !== undefined) throw new Error("preview: give either port or port_field");
    if (port_field !== undefined) {
      const f = typeof port_field === "string" ? byName.get(port_field) : undefined;
      if (!f) throw new Error(`preview.port_field "${String(port_field)}" is not a field`);
      if (f.type !== "int") throw new Error(`preview.port_field "${f.name}" must be an int field`);
      preview = { port: typeof f.default === "number" ? f.default : null, port_field: f.name };
    } else if (port !== undefined) {
      if (!Number.isInteger(port) || (port as number) < 1 || (port as number) > 65535) throw new Error("preview.port must be an integer in 1..65535");
      preview = { port: port as number, port_field: null };
    } else throw new Error("preview: give port or port_field");
  }

  let claims: string[] = [];
  if (doc.claims_when !== undefined && doc.claims_when !== null) {
    if (!isObj(doc.claims_when)) throw new Error("claims_when must be a map");
    const { present, ...rest } = doc.claims_when;
    if (Object.keys(rest).length) throw new Error(`claims_when: unknown key "${Object.keys(rest)[0]}"`);
    claims = typeof present === "string" ? [present] : Array.isArray(present) && present.every((p) => typeof p === "string") ? (present as string[]) : [];
    if (!claims.length) throw new Error("claims_when.present must be a field name or a list of them");
    for (const c of claims) if (!byName.has(c)) throw new Error(`claims_when.present "${c}" is not a field`);
  }

  const serviceRefs = parseServiceRefs(doc.services);

  return { name, description: doc.description.trim(), image, cmd, idle_timeout_s: idle, preview, services: serviceRefs.map((r) => r.name ?? r.use), fields, claims, serviceRefs };
}

function parseFields(raw: unknown): FieldSpec[] {
  if (raw === undefined || raw === null) return [];
  if (!isObj(raw)) throw new Error("fields must be a map");
  const out: FieldSpec[] = [];
  const envs = new Map<string, string>();
  for (const [path, spec] of Object.entries(raw)) {
    const at = `fields.${path}`;
    if (!PATH_RE.test(path)) throw new Error(`${at}: field names are lowercase identifiers, dotted for nesting`);
    if (!isObj(spec)) throw new Error(`${at} must be a map`);
    for (const k of Object.keys(spec)) if (!FIELD_KEYS.has(k)) throw new Error(`${at}: unknown key "${k}"`);
    const type = spec.type as FieldType;
    if (!TYPES.includes(type)) throw new Error(`${at}.type must be one of ${TYPES.join(", ")}`);
    const env = spec.env === undefined ? path.toUpperCase().replace(/[^A-Z0-9_]/g, "_") : spec.env;
    if (typeof env !== "string" || !ENV_RE.test(env)) throw new Error(`${at}.env must be an environment variable name`);
    if (RESERVED_ENV.has(env)) throw new Error(`${at}.env "${env}" is reserved`);
    if (envs.has(env)) throw new Error(`${at}.env "${env}" is already used by fields.${envs.get(env)}`);
    envs.set(env, path);
    const f: FieldSpec = { name: path, type, env, required: bool(at, "required", spec.required), secret: bool(at, "secret", spec.secret) };
    if (spec.description !== undefined) { if (typeof spec.description !== "string") throw new Error(`${at}.description must be a string`); f.description = spec.description; }
    if (spec.multiline !== undefined) { if (type !== "string") throw new Error(`${at}.multiline only applies to string fields`); f.multiline = bool(at, "multiline", spec.multiline); }
    if (type === "enum") {
      if (!Array.isArray(spec.values) || !spec.values.length || !spec.values.every((v) => typeof v === "string")) throw new Error(`${at}.values must be a non-empty list of strings`);
      f.values = spec.values as string[];
    } else if (spec.values !== undefined) throw new Error(`${at}.values only applies to enum fields`);
    if (type === "int") {
      if (spec.min !== undefined) { if (!Number.isInteger(spec.min)) throw new Error(`${at}.min must be an integer`); f.min = spec.min as number; }
      if (spec.max !== undefined) { if (!Number.isInteger(spec.max)) throw new Error(`${at}.max must be an integer`); f.max = spec.max as number; }
      if (f.min !== undefined && f.max !== undefined && f.min > f.max) throw new Error(`${at}: min > max`);
    } else if (spec.min !== undefined || spec.max !== undefined) throw new Error(`${at}.min/max only apply to int fields`);
    if (spec.default !== undefined && spec.default !== null) {
      if (f.required) throw new Error(`${at}: a required field cannot have a default`);
      f.default = checkDefault(at, f, spec.default);
    }
    out.push(f);
  }
  return out;
}

function checkDefault(at: string, f: FieldSpec, v: unknown): string | number | boolean {
  switch (f.type) {
    case "string": if (typeof v !== "string") throw new Error(`${at}.default must be a string`); return v;
    case "url": if (typeof v !== "string" || !/^https?:\/\//.test(v)) throw new Error(`${at}.default must be an http(s) url`); return v.replace(/\/+$/, "");
    case "bool": if (typeof v !== "boolean") throw new Error(`${at}.default must be a boolean`); return v;
    case "enum": if (typeof v !== "string" || !f.values!.includes(v)) throw new Error(`${at}.default must be one of ${f.values!.join(", ")}`); return v;
    case "int":
      if (!Number.isInteger(v) || (f.min !== undefined && (v as number) < f.min) || (f.max !== undefined && (v as number) > f.max)) throw new Error(`${at}.default must be an integer in ${f.min ?? "-inf"}..${f.max ?? "inf"}`);
      return v as number;
  }
}

function parseServiceRefs(raw: unknown): ServiceRef[] {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw)) throw new Error("services must be a list");
  return raw.map((item, i) => {
    const at = `services[${i}]`;
    if (typeof item === "string") return { use: item };
    if (!isObj(item)) throw new Error(`${at} must be a catalog name or a map`);
    for (const k of Object.keys(item)) if (!["use", "name", "env"].includes(k)) throw new Error(`${at}: unknown key "${k}"`);
    if (typeof item.use !== "string" || !item.use) throw new Error(`${at}.use is required`);
    const ref: ServiceRef = { use: item.use };
    if (item.name !== undefined) { if (typeof item.name !== "string") throw new Error(`${at}.name must be a string`); ref.name = item.name; }
    if (item.env !== undefined) {
      if (!isObj(item.env) || !Object.values(item.env).every((v) => typeof v === "string")) throw new Error(`${at}.env must be a map of strings`);
      ref.env = item.env as Record<string, string>;
    }
    return ref;
  });
}

const bool = (at: string, key: string, v: unknown): boolean => {
  if (v === undefined) return false;
  if (typeof v !== "boolean") throw new Error(`${at}.${key} must be true or false`);
  return v;
};
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
