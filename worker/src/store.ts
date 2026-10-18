import {
  type Authorization,
  HttpError,
  type Identity,
  object,
  type Receipt,
  type Submission,
  text,
} from "@/model.ts";

type Binding = string | number | null;
export interface Database {
  query(sql: string, ...bindings: Binding[]): unknown[];
  transaction<T>(run: () => T): T;
}

const schema = `
CREATE TABLE IF NOT EXISTS submissions (
 id TEXT PRIMARY KEY, publisher TEXT NOT NULL, version_key TEXT NOT NULL UNIQUE,
 body_hash TEXT NOT NULL, receipt TEXT NOT NULL, status TEXT NOT NULL,
 created INTEGER NOT NULL, lease_id TEXT, lease_owner TEXT, lease_until INTEGER,
 attempts INTEGER NOT NULL DEFAULT 0, detail TEXT
);
CREATE INDEX IF NOT EXISTS pending ON submissions(status, created);
CREATE INDEX IF NOT EXISTS publisher_status ON submissions(publisher, status);
CREATE TABLE IF NOT EXISTS replay (token_key TEXT PRIMARY KEY, body_hash TEXT NOT NULL, submission_id TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS rates (publisher TEXT NOT NULL, hour INTEGER NOT NULL, count INTEGER NOT NULL, PRIMARY KEY(publisher, hour));
`;

function row(value: unknown): Record<string, unknown> {
  return object(value);
}
function count(value: unknown): number {
  const n = row(value).n;
  if (typeof n !== "number") throw new Error("invalid database count");
  return n;
}
function response(value: Record<string, unknown>): { id: string; status: string } {
  return { id: text(value.id), status: text(value.status) };
}

/** All state transitions run in one SQLite transaction; no awaits inside a transition. */
export class InboxStore {
  constructor(private readonly db: Database) {
    db.query(schema);
  }

  submit(s: Submission, auth: Authorization, hash: string, now: number): { id: string; status: string } {
    return this.db.transaction(() => {
      this.db.query("DELETE FROM replay WHERE expires < ?", now - 60);
      this.db.query("DELETE FROM rates WHERE hour < ?", Math.floor(now / 3600) - 48);
      const publisher = JSON.stringify([auth.identity.issuer, auth.identity.repository_id]);
      const version = JSON.stringify([auth.identity.issuer, auth.identity.repository_id, s.tag, s.asset]);
      const token = JSON.stringify([auth.identity.issuer, auth.jti]);
      const replay =
        this.db.query("SELECT body_hash, submission_id FROM replay WHERE token_key = ?", token)[0];
      if (replay !== undefined) {
        const prior = row(replay);
        if (prior.body_hash !== hash) {
          throw new HttpError(409, "OIDC token already used for a different submission");
        }
        return response(
          row(this.db.query("SELECT id, status FROM submissions WHERE id = ?", text(prior.submission_id))[0]),
        );
      }
      const hour = Math.floor(now / 3600);
      const rate =
        this.db.query("SELECT count AS n FROM rates WHERE publisher = ? AND hour = ?", publisher, hour)[0];
      if (rate !== undefined && count(rate) >= 30) {
        throw new HttpError(429, "publisher hourly quota exceeded");
      }
      const previous =
        this.db.query("SELECT id, status, body_hash FROM submissions WHERE version_key = ?", version)[0];
      if (previous !== undefined) {
        const prior = row(previous);
        if (prior.body_hash !== hash) {
          throw new HttpError(409, "version already bound to different submission bytes");
        }
        this.db.query("INSERT INTO replay VALUES (?, ?, ?, ?)", token, hash, text(prior.id), auth.expires);
        this.db.query(
          "INSERT INTO rates VALUES (?,?,1) ON CONFLICT(publisher,hour) DO UPDATE SET count=count+1",
          publisher,
          hour,
        );
        return response(prior);
      }
      if (
        count(
            this.db.query(
              "SELECT count(*) AS n FROM submissions WHERE publisher = ? AND status IN ('pending', 'validating')",
              publisher,
            )[0],
          ) >= 10
        || count(
            this.db.query(
              "SELECT count(*) AS n FROM submissions WHERE status IN ('pending', 'validating')",
            )[0],
          ) >= 1000
      ) throw new HttpError(429, "inbox pending quota exceeded");
      const id = crypto.randomUUID();
      const receipt: Receipt = {
        schema_version: 1,
        id,
        submission: s,
        identity: auth.identity,
        authorized_at: new Date(now * 1000).toISOString(),
      };
      this.db.query(
        "INSERT INTO submissions (id,publisher,version_key,body_hash,receipt,status,created) VALUES (?,?,?,?,?,'pending',?)",
        id,
        publisher,
        version,
        hash,
        JSON.stringify(receipt),
        now,
      );
      this.db.query("INSERT INTO replay VALUES (?, ?, ?, ?)", token, hash, id, auth.expires);
      this.db.query(
        "INSERT INTO rates VALUES (?,?,1) ON CONFLICT(publisher,hour) DO UPDATE SET count=count+1",
        publisher,
        hour,
      );
      return { id, status: "pending" };
    });
  }

  claim(identity: Identity, now: number): { claim: null | { receipt: unknown; lease_id: string } } {
    return this.db.transaction(() => {
      this.db.query(
        "UPDATE submissions SET status='failed', detail='validator retry limit reached' WHERE status='validating' AND lease_until <= ? AND attempts >= 3",
        now,
      );
      const next = this.db.query(
        "SELECT id, receipt FROM submissions WHERE status='pending' OR (status='validating' AND lease_until <= ? AND attempts < 3) ORDER BY created,id LIMIT 1",
        now,
      )[0];
      if (next === undefined) return { claim: null };
      const value = row(next);
      const lease = crypto.randomUUID();
      this.db.query(
        "UPDATE submissions SET status='validating', lease_id=?, lease_owner=?, lease_until=?, attempts=attempts+1 WHERE id=?",
        lease,
        JSON.stringify(identity),
        now + 3600,
        text(value.id),
      );
      return { claim: { receipt: JSON.parse(text(value.receipt, 16384)), lease_id: lease } };
    });
  }

  complete(
    id: string,
    identity: Identity,
    lease: string,
    status: "published" | "rejected",
    detail: string | undefined,
    now: number,
  ): { id: string; status: string } {
    return this.db.transaction(() => {
      const found =
        this.db.query("SELECT id,status,lease_id,lease_owner,lease_until FROM submissions WHERE id=?", id)[0];
      if (found === undefined) throw new HttpError(404, "submission not found");
      const value = row(found);
      if (value.lease_id !== lease || value.lease_owner !== JSON.stringify(identity)) {
        throw new HttpError(403, "lease owner mismatch");
      }
      if (value.status === status) return response(value);
      if (
        value.status !== "validating" || typeof value.lease_until !== "number" || value.lease_until <= now
      ) throw new HttpError(409, "lease expired or already completed");
      this.db.query("UPDATE submissions SET status=?, detail=? WHERE id=?", status, detail ?? null, id);
      return { id, status };
    });
  }

  status(id: string): { id: string; status: string } {
    const found = this.db.query("SELECT id,status FROM submissions WHERE id=?", id)[0];
    if (found === undefined) throw new HttpError(404, "submission not found");
    return response(row(found));
  }
}
