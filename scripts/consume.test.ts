import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { claim, complete, recover } from '@/scripts/consume.ts';

const id = '00000000-0000-4000-8000-000000000001';
const leaseID = '00000000-0000-4000-8000-000000000002';

// Claims cross a job boundary through these files. An invalid or empty response
// must not create a handoff, and the authenticated token must never enter it.
test('claim saves only authenticated receipt and lease, and rejects malformed responses', async (t) => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-claim-'));
	t.after(() => rm(directory, { recursive: true, force: true }));
	const receipt = { schema_version: 1, id, submission: {}, identity: {} };
	const outputs: string[] = [];
	let reply: unknown = { claim: null };
	const client = {
		token: async () => 'secret-token',
		request: async (url: string, token: string, payload: unknown) => {
			assert.equal(url, 'https://registry.actionlint.kjanat.dev/v1/claims');
			assert.equal(token, 'secret-token');
			assert.deepEqual(payload, {});
			return reply;
		},
		output: (name: string, value: string) => {
			outputs.push(`${name}=${value}`);
		},
	};
	const handoff = join(directory, 'handoff');
	await claim(handoff, client);
	assert.deepEqual(outputs, ['claimed=false']);
	await assert.rejects(stat(handoff));
	for (
		const invalid of [
			{ claim: { receipt: { id: '../../evil' }, lease_id: leaseID } },
			{ claim: null, extra: true },
		]
	) {
		reply = invalid;
		await assert.rejects(claim(handoff, client));
		await assert.rejects(stat(handoff));
	}
	reply = { claim: { receipt, lease_id: leaseID } };
	await claim(handoff, client);
	assert.deepEqual(outputs, ['claimed=false', 'claimed=true']);
	assert.deepEqual(
		JSON.parse(await readFile(join(handoff, 'receipt.json'), 'utf8')),
		receipt,
	);
	assert.deepEqual(
		JSON.parse(await readFile(join(handoff, 'lease.json'), 'utf8')),
		{ id, lease_id: leaseID },
	);
	assert.doesNotMatch(
		await readFile(join(handoff, 'receipt.json'), 'utf8'),
		/secret-token/,
	);
});

// A matching deterministic report authorizes rejection. Missing reports and
// infrastructure failures request a retry using the same run-bound lease.
test('recovery rejects matching admission and publication conflicts, retries infrastructure failures', async (t) => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-recover-'));
	t.after(() => rm(directory, { recursive: true, force: true }));
	const path = join(directory, 'lease.json');
	const reportPath = join(directory, 'failure.json');
	await writeFile(path, JSON.stringify({ id, lease_id: leaseID }));
	const calls: { url: string; payload: unknown }[] = [];
	const client = {
		token: async () => 'secret-token',
		request: async (url: string, token: string, payload: unknown) => {
			assert.equal(token, 'secret-token');
			calls.push({ url, payload });
			return { id, status: url.endsWith('/retry') ? 'pending' : 'rejected' };
		},
		output: () => {},
	};
	await recover(path, [join(directory, 'missing.json')], client);
	assert.deepEqual(calls.pop(), {
		url: `https://registry.actionlint.kjanat.dev/v1/claims/${id}/retry`,
		payload: { lease_id: leaseID },
	});
	for (const mode of ['admit', 'merge']) {
		for (const disposition of ['rejected', 'retryable']) {
			await writeFile(
				reportPath,
				JSON.stringify({
					schema_version: 1,
					mode,
					submission_id: id,
					accepted: [],
					rejected: [{ disposition }],
				}),
			);
			await recover(path, [reportPath], client);
			assert.deepEqual(calls.pop(), {
				url: `https://registry.actionlint.kjanat.dev/v1/claims/${id}/${
					disposition === 'rejected' ? 'complete' : 'retry'
				}`,
				payload: disposition === 'rejected'
					? { lease_id: leaseID, status: 'rejected' }
					: { lease_id: leaseID },
			});
		}
	}
	for (
		const change of [
			{ submission_id: leaseID },
			{ accepted: [{}] },
			{ mode: 'inspect' },
			{ schema_version: 2 },
			{ rejected: [{ disposition: 'unknown' }] },
		]
	) {
		await writeFile(
			reportPath,
			JSON.stringify({
				schema_version: 1,
				mode: 'admit',
				submission_id: id,
				accepted: [],
				rejected: [{ disposition: 'rejected' }],
				...change,
			}),
		);
		await assert.rejects(recover(path, [reportPath], client));
		assert.equal(calls.length, 0);
	}
});

// A completion response for another request must not count as success. Check
// the request's lease binding and both response fields for publication completion.
test('completion binds the lease and checks the returned submission and status', async (t) => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-complete-'));
	t.after(() => rm(directory, { recursive: true, force: true }));
	const path = join(directory, 'lease.json');
	await writeFile(path, JSON.stringify({ id, lease_id: leaseID }));
	let reply = { id, status: 'published' };
	const client = {
		token: async () => 'secret-token',
		request: async (url: string, _token: string, payload: unknown) => {
			assert.equal(
				url,
				`https://registry.actionlint.kjanat.dev/v1/claims/${id}/complete`,
			);
			assert.deepEqual(payload, { lease_id: leaseID, status: 'published' });
			return reply;
		},
		output: () => {},
	};
	await complete(path, 'published', client);
	for (
		const invalid of [
			{ id: leaseID, status: 'published' },
			{ id, status: 'validating' },
		]
	) {
		reply = invalid;
		await assert.rejects(
			complete(path, 'published', client),
			/Unexpected completion response/,
		);
	}
});
