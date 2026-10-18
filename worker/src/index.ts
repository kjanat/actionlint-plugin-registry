import { bearer, consumer, type Policy, publisher, verify } from "@/auth.ts";
import { json, preflight } from "@/http.ts";
import { body, digest, exactKeys, HttpError, object, submission, text } from "@/model.ts";
import { InboxStore } from "@/store.ts";
import { DurableObject } from "cloudflare:workers";

interface Env extends Policy {
  INBOX: DurableObjectNamespace<PublicationInbox>;
}

export class PublicationInbox extends DurableObject<Env> {
  private readonly store: InboxStore;
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.store = new InboxStore({
      query: (sql, ...bindings) => ctx.storage.sql.exec(sql, ...bindings).toArray(),
      transaction: run => ctx.storage.transactionSync(run),
    });
  }

  override async fetch(request: Request): Promise<Response> {
    try {
      const url = new URL(request.url);
      const now = Math.floor(Date.now() / 1000);
      if (url.origin !== this.env.REGISTRY_ORIGIN || url.search !== "") {
        throw new HttpError(400, "invalid endpoint");
      }
      if (request.method === "GET" && url.pathname === "/health") return json({ status: "ok" });
      const statusID = /^\/v1\/submissions\/([0-9a-f-]{36})$/u.exec(url.pathname)?.[1];
      if (request.method === "GET" && statusID) return json(this.store.status(statusID));
      if (request.method !== "POST") throw new HttpError(404, "endpoint not found");
      if (url.pathname === "/v1/submissions") {
        const parsed = submission(await body(request));
        const auth = publisher(await verify(bearer(request), this.env.REGISTRY_ORIGIN), parsed);
        return json(this.store.submit(parsed, auth, await digest(JSON.stringify(parsed)), now), 202);
      }
      const completionID = /^\/v1\/claims\/([0-9a-f-]{36})\/complete$/u.exec(url.pathname)?.[1];
      if (url.pathname === "/v1/claims" || completionID) {
        const identity = consumer(
          await verify(bearer(request), `${this.env.REGISTRY_ORIGIN}/validator`),
          this.env,
        );
        if (!completionID) return json(this.store.claim(identity, now));
        const input = object(await body(request));
        exactKeys(input, ["lease_id", "status", "detail"]);
        if (input.status !== "published" && input.status !== "rejected") {
          throw new HttpError(400, "invalid completion status");
        }
        const detail = input.detail === undefined ? undefined : text(input.detail, 1024);
        return json(
          this.store.complete(completionID, identity, text(input.lease_id, 36), input.status, detail, now),
        );
      }
      throw new HttpError(404, "endpoint not found");
    } catch (error) {
      if (error instanceof HttpError) return json({ error: error.message }, error.status);
      return json({ error: "inbox unavailable" }, 503);
    }
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const rejected = preflight(request, env);
    if (rejected) return rejected;
    return env.INBOX.getByName("publication-inbox-v1").fetch(request);
  },
} satisfies ExportedHandler<Env>;
