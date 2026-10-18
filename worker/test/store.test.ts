import { Database } from 'bun:sqlite';
import { afterEach, expect, test } from 'bun:test';
import { publisher } from '#auth';
import type { Authorization, Identity } from '#model';
import { digest, object, submission, text } from '#model';
import { InboxStore } from '#store';

const open: Database[] = [];
afterEach(() => {
	for (const db of open.splice(0)) db.close();
});
function store(): InboxStore {
	const db = new Database(':memory:');
	open.push(db);
	return new InboxStore({
		query: (sql, ...bindings) => {
			if (sql.includes('CREATE TABLE')) {
				db.exec(sql);
				return [];
			}
			return db.query(sql).all(...bindings);
		},
		transaction: (run) => db.transaction(run)(),
	});
}
const s = submission({
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
const auth: Authorization = { identity, jti: 'one', expires: 10000 };

test('replay is idempotent; changed body or fresh-token digest/kind replacement rejected', async () => {
	const inbox = store();
	const hash = await digest(JSON.stringify(s));
	const accepted = inbox.submit(s, auth, hash, 100);
	expect(inbox.submit(s, auth, hash, 101)).toEqual(accepted);
	const changed = { ...s, sha256: 'b'.repeat(64) };
	expect(() => inbox.submit(changed, auth, 'changed', 102)).toThrow(
		'OIDC token has already been used',
	);
	expect(() => inbox.submit(changed, { ...auth, jti: 'two' }, 'changed', 102)).toThrow(
		'version is already bound',
	);
	expect(() =>
		inbox.submit(
			submission({ ...s, kind: 'component' }),
			{ ...auth, jti: 'three' },
			'changed',
			102,
		)
	).toThrow('version is already bound');
});

test('claims have owner/run-specific leases; completion is idempotent', () => {
	const inbox = store();
	const accepted = inbox.submit(s, auth, 'hash', 100);
	const claim = inbox.claim(identity, 101).claim;
	if (!claim) throw new Error('missing claim');
	expect(inbox.claim(identity, 102).claim).toBeNull();
	expect(() =>
		inbox.complete(
			accepted.id,
			{ ...identity, run_id: 'other' },
			claim.lease_id,
			'published',
			undefined,
			103,
		)
	).toThrow('lease owner');
	expect(
		inbox.complete(
			accepted.id,
			{ ...identity },
			claim.lease_id,
			'published',
			'private detail',
			103,
		).status,
	).toBe('published');
	expect(
		inbox.complete(
			accepted.id,
			identity,
			claim.lease_id,
			'published',
			undefined,
			2000,
		).status,
	).toBe('published');
	expect(inbox.status(accepted.id)).toEqual({
		id: accepted.id,
		status: 'published',
	});
});

test('expired lease cannot complete; three expired attempts terminate', () => {
	const inbox = store();
	const accepted = inbox.submit(s, auth, 'hash', 100);
	const first = inbox.claim(identity, 100).claim;
	if (!first) throw new Error('missing claim');
	expect(() =>
		inbox.complete(
			accepted.id,
			identity,
			first.lease_id,
			'published',
			undefined,
			3700,
		)
	).toThrow('expired');
	expect(inbox.claim(identity, 3700).claim).not.toBeNull();
	expect(inbox.claim(identity, 7300).claim).not.toBeNull();
	expect(inbox.claim(identity, 10900).claim).toBeNull();
	expect(inbox.status(accepted.id).status).toBe('failed');
});

test('pending quota and fresh-token hourly quota are bounded', () => {
	const inbox = store();
	for (let i = 0; i < 10; i++) {
		inbox.submit(
			{ ...s, tag: `v${i}` },
			{ ...auth, jti: `token${i}` },
			`hash${i}`,
			100,
		);
	}
	expect(() =>
		inbox.submit(
			{ ...s, tag: 'v11' },
			{ ...auth, jti: 'token11' },
			'hash11',
			100,
		)
	).toThrow('pending quota');
	const second = store();
	for (let i = 0; i < 30; i++) {
		second.submit(s, { ...auth, jti: `token${i}` }, 'hash', 100);
	}
	expect(() => second.submit(s, { ...auth, jti: 'token31' }, 'hash', 100)).toThrow('hourly quota');
});

test('generated receipt matches the Go admission protocol fixture', async () => {
	const fixture = object(
		JSON.parse(
			await Bun.file(
				new URL(
					'../../internal/registry/testdata/github-receipt.json',
					import.meta.url,
				),
			).text(),
		),
	);
	const input = submission(fixture.submission);
	const expected = object(fixture.identity);
	const authorized = publisher(
		{
			iss: text(expected.issuer),
			sub: text(expected.subject),
			repository_id: expected.repository_id,
			repository_owner_id: expected.owner_id,
			repository: expected.repository,
			ref: expected.ref,
			sha: expected.commit,
			workflow_ref: expected.workflow,
			run_id: '1',
			run_attempt: '1',
			repository_visibility: 'public',
			ref_type: 'tag',
			event_name: 'push',
			jti: 'fixture',
			iat: 1767225600,
			exp: 1767225900,
		},
		input,
	);
	const inbox = store();
	inbox.submit(
		input,
		authorized,
		await digest(JSON.stringify(input)),
		1767225600,
	);
	const claim = inbox.claim(identity, 1767225601).claim;
	if (!claim) throw new Error('missing fixture claim');
	const generated = object(claim.receipt);
	expect(new Date(text(generated.authorized_at)).getTime()).toBe(
		new Date(text(fixture.authorized_at)).getTime(),
	);
	expect(
		object({
			...generated,
			id: fixture.id,
			authorized_at: fixture.authorized_at,
		}),
	).toEqual(fixture);
});
