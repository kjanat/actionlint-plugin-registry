import { DurableObject } from 'cloudflare:workers';
import type { Policy } from '#auth';
import { bearer, consumer, publisher, verify } from '#auth';
import type { GitHubAppConfig } from '#github-app';
import { dispatchAdmission } from '#github-app';
import { json, preflight } from '#http';
import { body, digest, exactKeys, HttpError, object, submission, text } from '#model';
import { DispatchOutbox, dispatchNotification, submitWithWakeup } from '#outbox';
import type { Database } from '#store';
import { InboxStore } from '#store';

interface Env extends Policy, GitHubAppConfig {
	INBOX: DurableObjectNamespace<PublicationInbox>;
}

export class PublicationInbox extends DurableObject<Env> {
	private readonly store: InboxStore;
	private readonly outbox: DispatchOutbox;
	constructor(ctx: DurableObjectState, env: Env) {
		super(ctx, env);
		const database: Database = {
			query: (sql, ...bindings) => ctx.storage.sql.exec(sql, ...bindings).toArray(),
			transaction: (run) => ctx.storage.transactionSync(run),
		};
		this.store = new InboxStore(database);
		this.outbox = new DispatchOutbox(database);
	}

	private async scheduleAlarm(timestamp: number): Promise<void> {
		await this.ctx.blockConcurrencyWhile(async () => {
			const current = await this.ctx.storage.getAlarm();
			if (current === null || timestamp < current) {
				await this.ctx.storage.setAlarm(timestamp);
			}
		});
	}

	override async alarm(): Promise<void> {
		await dispatchNotification(
			this.outbox,
			(timestamp) => this.scheduleAlarm(timestamp),
			() => dispatchAdmission(this.env),
			Math.floor(Date.now() / 1000),
		);
	}

	override async fetch(request: Request): Promise<Response> {
		try {
			const url = new URL(request.url);
			const now = Math.floor(Date.now() / 1000);
			if (url.origin !== this.env.REGISTRY_ORIGIN || url.search !== '') {
				throw new HttpError(400, 'The request endpoint is invalid.');
			}
			if (request.method === 'GET' && url.pathname === '/health') {
				return json({ status: 'ok' });
			}
			const statusID = /^\/v1\/submissions\/([0-9a-f-]{36})$/u.exec(
				url.pathname,
			)?.[1];
			if (request.method === 'GET' && statusID) {
				return json(this.store.status(statusID));
			}
			if (request.method !== 'POST') {
				throw new HttpError(404, 'The requested endpoint does not exist.');
			}
			if (url.pathname === '/v1/submissions') {
				const parsed = submission(await body(request));
				const auth = publisher(
					await verify(bearer(request), this.env.REGISTRY_ORIGIN),
					parsed,
				);
				const hash = await digest(JSON.stringify(parsed));
				return json(
					await submitWithWakeup(
						(timestamp) => this.scheduleAlarm(timestamp),
						now,
						() => this.store.submit(parsed, auth, hash, now),
					),
					202,
				);
			}
			const completionID = /^\/v1\/claims\/([0-9a-f-]{36})\/complete$/u.exec(
				url.pathname,
			)?.[1];
			if (url.pathname === '/v1/claims' || completionID) {
				const identity = consumer(
					await verify(
						bearer(request),
						`${this.env.REGISTRY_ORIGIN}/validator`,
					),
					this.env,
				);
				if (!completionID) return json(this.store.claim(identity, now));
				const input = object(await body(request));
				exactKeys(input, ['lease_id', 'status', 'detail']);
				if (input.status !== 'published' && input.status !== 'rejected') {
					throw new HttpError(400, 'The completion status must be published or rejected.');
				}
				const detail = input.detail === undefined ? undefined : text(input.detail, 1024);
				return json(
					this.store.complete(
						completionID,
						identity,
						text(input.lease_id, 36),
						input.status,
						detail,
						now,
					),
				);
			}
			throw new HttpError(404, 'The requested endpoint does not exist.');
		} catch (error) {
			if (error instanceof HttpError) {
				return json({ error: error.message }, error.status);
			}
			return json({ error: 'The publication inbox is temporarily unavailable.' }, 503);
		}
	}
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		const rejected = preflight(request, env);
		if (rejected) return rejected;
		return env.INBOX.getByName('publication-inbox-v1').fetch(request);
	},
} satisfies ExportedHandler<Env>;
