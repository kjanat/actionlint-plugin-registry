import { Database } from 'bun:sqlite';
import { afterEach, expect, spyOn, test } from 'bun:test';
import { DispatchFailure } from '#github-app';
import type { Authorization, Identity } from '#model';
import { submission } from '#model';
import { DispatchOutbox, dispatchNotification, submitWithWakeup } from '#outbox';
import type { Database as StoreDatabase } from '#store';
import { InboxStore } from '#store';

const open: Database[] = [];
afterEach(() => {
	for (const database of open.splice(0)) database.close();
});

function fixture(failNotification = false) {
	const database = new Database(':memory:');
	open.push(database);
	const adapter: StoreDatabase = {
		query(sql, ...bindings) {
			if (sql.includes('CREATE TABLE')) {
				database.exec(sql);
				return [];
			}
			if (failNotification && sql.startsWith('INSERT INTO dispatch_outbox')) {
				throw new Error('The notification could not be persisted.');
			}
			return database.query(sql).all(...bindings);
		},
		transaction: (run) => database.transaction(run)(),
	};
	return { database, inbox: new InboxStore(adapter), outbox: new DispatchOutbox(adapter) };
}

const input = submission({
	schema_version: 1,
	provider: 'github',
	repository: 'acme/plugin',
	tag: 'v1',
	asset: 'plugin.wasm',
	sha256: 'a'.repeat(64),
	kind: 'core',
});
const identity: Identity = {
	issuer: 'https://token.actions.githubusercontent.com',
	subject: 'repo:acme/plugin:ref:refs/tags/v1',
	repository_id: '10',
	owner_id: '20',
	repository: 'acme/plugin',
	ref: 'refs/tags/v1',
	commit: 'a'.repeat(40),
	workflow: 'acme/plugin/.github/workflows/release.yml@refs/tags/v1',
	run_id: '30:1',
};
const auth: Authorization = { identity, jti: 'one', expires: 1000 };

test('a persisted alarm precedes acceptance; alarm failure leaves no submission', async () => {
	const { inbox, database } = fixture();
	const events: string[] = [];
	await submitWithWakeup(
		async (timestamp) => {
			expect(timestamp).toBe(101000);
			expect(database.query('SELECT id FROM submissions').all()).toEqual([]);
			events.push('alarm');
		},
		100,
		() => {
			events.push('submit');
			return inbox.submit(input, auth, 'hash', 100);
		},
	);
	expect(events).toEqual(['alarm', 'submit']);
	await expect(submitWithWakeup(
		async () => {
			throw new Error('The alarm could not be persisted.');
		},
		100,
		() => inbox.submit({ ...input, tag: 'v2' }, { ...auth, jti: 'two' }, 'two', 100),
	)).rejects.toThrow('alarm');
	expect(database.query('SELECT id FROM submissions').all()).toHaveLength(1);
});

test('notification failure rolls back submission, replay and rate state', () => {
	const { inbox, database } = fixture(true);
	expect(() => inbox.submit(input, auth, 'hash', 100)).toThrow('notification');
	for (const table of ['submissions', 'replay', 'rates', 'dispatch_outbox']) {
		expect(database.query(`SELECT * FROM ${table}`).all()).toEqual([]);
	}
});

test('token replay cannot redispatch, but a fresh pending retry can repair an unclaimed delivery', async () => {
	const { inbox, outbox, database } = fixture();
	const accepted = inbox.submit(input, auth, 'hash', 100);
	expect(inbox.submit(input, auth, 'hash', 101)).toEqual(accepted);
	expect(inbox.submit(input, { ...auth, jti: 'two' }, 'hash', 101)).toEqual(accepted);
	expect(database.query('SELECT * FROM dispatch_outbox').all()).toHaveLength(1);
	let dispatched = 0;
	await dispatchNotification(outbox, async () => {}, async () => {
		dispatched++;
	}, 101);
	inbox.submit(input, auth, 'hash', 102);
	await dispatchNotification(outbox, async () => {}, async () => {
		dispatched++;
	}, 102);
	expect(dispatched).toBe(1);
	expect(outbox.next(102)).toBeNull();
	expect(inbox.submit(input, { ...auth, jti: 'three' }, 'hash', 103)).toEqual(accepted);
	await dispatchNotification(outbox, async () => {}, async () => {
		dispatched++;
	}, 103);
	expect(dispatched).toBe(2);
	expect(inbox.status(accepted.id).status).toBe('pending');
});

test('a fresh authenticated retry repairs exhausted dispatch with the original claimable receipt', async () => {
	const { inbox, outbox, database } = fixture();
	const accepted = inbox.submit(input, auth, 'hash', 100);
	const original = database.query('SELECT receipt FROM submissions').get();
	for (const now of [100, 130, 190, 310, 550]) {
		await dispatchNotification(outbox, async () => {}, async () => {
			throw new DispatchFailure({ code: 'http-error', stage: 'workflow-dispatch', status: 503 });
		}, now);
	}
	expect(outbox.next(580)).toBeNull();
	expect(database.query('SELECT * FROM dispatch_outbox').all()).toEqual([]);
	inbox.submit(input, auth, 'hash', 600);
	expect(outbox.next(600)).toBeNull();
	expect(inbox.submit(input, { ...auth, jti: 'retry' }, 'hash', 601)).toEqual(accepted);
	let dispatched = 0;
	await dispatchNotification(outbox, async () => {}, async () => {
		dispatched++;
	}, 602);
	expect(dispatched).toBe(1);
	expect(database.query('SELECT receipt FROM submissions').get()).toEqual(original);
	expect(inbox.claim(identity, 603).claim?.receipt).toMatchObject({
		id: accepted.id,
		submission: input,
		identity,
	});
});

test('fresh pending retries preserve active backoff and the final in-flight attempt', () => {
	const { inbox, outbox, database } = fixture();
	inbox.submit(input, auth, 'hash', 100);
	expect(outbox.reserve(100)?.attempt).toBe(1);
	inbox.submit(input, { ...auth, jti: 'active' }, 'hash', 101);
	expect(outbox.next(101)).toBe(130);
	database.query('UPDATE dispatch_outbox SET attempts=5').run();
	inbox.submit(input, { ...auth, jti: 'final' }, 'hash', 102);
	expect(database.query('SELECT attempts,next_attempt FROM dispatch_outbox').all()).toEqual([
		{ attempts: 5, next_attempt: 130 },
	]);
});

test('fresh pending retries repair expired notifications before alarm cleanup', () => {
	const { inbox, outbox } = fixture();
	const accepted = inbox.submit(input, auth, 'hash', 100);
	expect(inbox.submit(input, { ...auth, jti: 'retry' }, 'hash', 3700)).toEqual(accepted);
	expect(outbox.reserve(3700)?.attempt).toBe(1);
});

test('fresh retries never dispatch validating, published, or rejected submissions', () => {
	for (const status of ['validating', 'published', 'rejected']) {
		const { inbox, outbox, database } = fixture();
		const accepted = inbox.submit(input, auth, 'hash', 100);
		inbox.claim(identity, 101);
		database.query('UPDATE submissions SET status=?').run(status);
		expect(inbox.submit(input, { ...auth, jti: 'retry' }, 'hash', 102)).toEqual(inbox.status(accepted.id));
		expect(outbox.next(102)).toBeNull();
	}
});

test('failed dispatch reserves exponential backoff before network and stops after five attempts', async () => {
	const { inbox, outbox } = fixture();
	inbox.submit(input, auth, 'hash', 100);
	let dispatched = 0;
	const alarms: number[] = [];
	for (const [index, now] of [100, 130, 190, 310, 550].entries()) {
		await dispatchNotification(outbox, async (timestamp) => {
			alarms.push(timestamp);
		}, async () => {
			expect(alarms.at(-1)).toBe((now + 30) * 1000);
			expect(outbox.reserve(now)).toBeNull();
			dispatched++;
			throw new Error('A remote response containing a secret must not escape.');
		}, now);
		expect(dispatched).toBe(index + 1);
	}
	await dispatchNotification(outbox, async () => {}, async () => {
		dispatched++;
	}, 1030);
	expect(dispatched).toBe(5);
	expect(outbox.next(1030)).toBeNull();
});

test('an alarm failure does not consume a dispatch attempt', async () => {
	const { inbox, outbox, database } = fixture();
	inbox.submit(input, auth, 'hash', 100);
	await expect(dispatchNotification(outbox, async () => {
		throw new Error('The alarm could not be persisted.');
	}, async () => {
		throw new Error('Dispatch must not run.');
	}, 100)).rejects.toThrow('alarm');
	expect(database.query('SELECT attempts FROM dispatch_outbox').all()).toEqual([{ attempts: 0 }]);
});

test('failed dispatch logs only one sanitized classification and leaves its retry durable', async () => {
	for (const failure of [
		new DispatchFailure({ code: 'http-error', stage: 'workflow-dispatch', status: 403 }),
		new DispatchFailure({ code: 'invalid', stage: 'signing' }),
		new Error('secret token, private key and signed request URL'),
	]) {
		const { inbox, outbox } = fixture();
		const accepted = inbox.submit(input, auth, 'hash', 100);
		const messages: unknown[] = [];
		const log = spyOn(console, 'error').mockImplementation((message: unknown) => {
			messages.push(message);
		});
		try {
			await dispatchNotification(outbox, async () => {}, async () => {
				throw failure;
			}, 100);
			expect(messages).toEqual([JSON.stringify({
				event: 'registry-dispatch-failed',
				submission: accepted.id,
				attempt: 1,
				failure: failure instanceof DispatchFailure ? failure.details : { code: 'unexpected' },
			})]);
			expect(JSON.stringify(messages)).not.toContain('secret');
			expect(outbox.next(100)).toBe(130);
		} finally {
			log.mockRestore();
		}
	}
});

test('consumer claims and expired notifications suppress dispatch', async () => {
	const first = fixture();
	first.inbox.submit(input, auth, 'hash', 100);
	expect(first.inbox.claim(identity, 101).claim).not.toBeNull();
	expect(first.inbox.claim(identity, 102).claim).toBeNull();
	let dispatched = 0;
	await dispatchNotification(first.outbox, async () => {}, async () => {
		dispatched++;
	}, 103);
	const second = fixture();
	second.inbox.submit(input, auth, 'hash', 100);
	await dispatchNotification(second.outbox, async () => {}, async () => {
		dispatched++;
	}, 3700);
	expect(dispatched).toBe(0);
	expect(first.outbox.next(103)).toBeNull();
	expect(second.outbox.next(3700)).toBeNull();
});

test('a concurrent consumer claim during alarm persistence prevents dispatch', async () => {
	const { inbox, outbox } = fixture();
	inbox.submit(input, auth, 'hash', 100);
	let dispatched = 0;
	await dispatchNotification(outbox, async () => {
		inbox.claim(identity, 100);
	}, async () => {
		dispatched++;
	}, 100);
	expect(dispatched).toBe(0);
});

test('a crash after reservation leaves a future retry and cannot double-reserve', () => {
	const { inbox, outbox } = fixture();
	inbox.submit(input, auth, 'hash', 100);
	expect(outbox.reserve(100)).not.toBeNull();
	expect(outbox.reserve(100)).toBeNull();
	expect(outbox.next(101)).toBe(130);
	expect(outbox.reserve(130)?.attempt).toBe(2);
});
