export interface CpConfig {
  port: number;
  serviceToken: string;
  secret: string;
  publicUrl: string;       // http(s)://cp.example.com  (what browsers use)
  previewDomain: string;   // preview.cp.example.com   (wildcard)
  defaultImage: string;
  dbPath: string;
  dev: boolean;
}

export function loadConfig(env = process.env): CpConfig {
  let serviceToken = env.CP_SERVICE_TOKEN;
  if (!serviceToken) {
    serviceToken = "dev-token";
    console.warn('WARN  CP_SERVICE_TOKEN is not set; using the default "dev-token". Do not run this way outside local dev.');
  }
  const port = Number(env.CP_PORT ?? 8080);
  const publicUrl = (env.CP_PUBLIC_URL ?? `http://localhost:${port}`).replace(/\/+$/, "");
  return {
    port,
    serviceToken,
    secret: env.CP_SECRET ?? serviceToken,
    publicUrl,
    previewDomain: env.CP_PREVIEW_DOMAIN ?? "preview.localhost",
    defaultImage: env.CP_DEFAULT_IMAGE ?? "cp-sandbox:latest",
    dbPath: env.CP_DB ?? "cp.db",
    dev: env.CP_DEV === "true" || env.CP_DEV === "1",
  };
}
