export class HttpError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export function object(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new HttpError(400, "expected object");
  }
  return Object.fromEntries(Object.entries(value));
}

export function text(value: unknown, limit = 512): string {
  if (
    typeof value !== "string" || value.length === 0 || value.length > limit
    || /[\u0000-\u001f\u007f]/u.test(value)
  ) {
    throw new HttpError(400, "invalid string");
  }
  return value;
}

export function exactKeys(value: Record<string, unknown>, keys: string[]): void {
  if (Object.keys(value).some(key => !keys.includes(key))) throw new HttpError(400, "unknown field");
}

export interface Submission {
  schema_version: 1;
  provider: "github" | "gitlab";
  repository: string;
  tag: string;
  asset: string;
  sha256: string;
  kind: "core" | "component";
}

export function submission(value: unknown): Submission {
  const v = object(value);
  exactKeys(v, ["schema_version", "provider", "repository", "tag", "asset", "sha256", "kind"]);
  if (
    v.schema_version !== 1 || (v.provider !== "github" && v.provider !== "gitlab")
    || (v.kind !== "core" && v.kind !== "component")
  ) {
    throw new HttpError(400, "invalid submission version, provider or kind");
  }
  const repository = text(v.repository, 256).toLowerCase();
  const parts = repository.split("/");
  if (
    parts.length < 2 || (v.provider === "github" && parts.length !== 2)
    || parts.some(p => !/^[a-z0-9][a-z0-9._-]*$/u.test(p) || p.endsWith(".git"))
  ) throw new HttpError(400, "invalid repository");
  const tag = text(v.tag, 128);
  const asset = text(v.asset, 128);
  const sha256 = text(v.sha256, 64);
  if (
    !/^[A-Za-z0-9][A-Za-z0-9._+-]*$/u.test(tag)
    || !/^[A-Za-z0-9][A-Za-z0-9._+-]*\.wasm(?:\.gz)?$/u.test(asset) || !/^[a-f0-9]{64}$/u.test(sha256)
  ) throw new HttpError(400, "invalid tag, asset or digest");
  return { schema_version: 1, provider: v.provider, repository, tag, asset, sha256, kind: v.kind };
}

export interface Identity {
  issuer: string;
  subject: string;
  repository_id: string;
  owner_id: string;
  repository: string;
  ref: string;
  commit: string;
  workflow: string;
  run_id: string;
}

export interface Receipt {
  schema_version: 1;
  id: string;
  submission: Submission;
  identity: Identity;
  authorized_at: string;
}

export interface Authorization {
  identity: Identity;
  jti: string;
  expires: number;
}

export async function body(request: Request): Promise<unknown> {
  if (request.headers.get("content-type")?.split(";")[0]?.trim() !== "application/json") {
    throw new HttpError(415, "application/json required");
  }
  if (request.body === null) throw new HttpError(400, "body required");
  const reader = request.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false });
  let size = 0;
  let result = "";
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      size += chunk.value.byteLength;
      if (size > 16384) {
        await reader.cancel();
        throw new HttpError(413, "body too large");
      }
      result += decoder.decode(chunk.value, { stream: true });
    }
    result += decoder.decode();
    return JSON.parse(result);
  } catch (error) {
    if (error instanceof HttpError) throw error;
    throw new HttpError(400, "invalid JSON body");
  } finally {
    reader.releaseLock();
  }
}

export async function digest(value: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(bytes), n => n.toString(16).padStart(2, "0")).join("");
}
