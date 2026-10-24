import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { checkCandidate, checkPublished } from '@/scripts/check.ts';
import { cleanEnvironment, execute } from '@/scripts/process.ts';

test('a report cannot replace candidate bytes or its delivery receipt', async () => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-report-'));
	try {
		const artifact = join(directory, 'plugin.wasm');
		await writeFile(artifact, 'candidate bytes');
		await assert.rejects(
			checkCandidate({
				actionlint: process.execPath,
				fixture: 'unused.ts',
				registry: 'unused',
				artifact,
				store: directory,
				report: artifact,
			}),
			/overwrite an input/,
		);
		assert.equal(await readFile(artifact, 'utf8'), 'candidate bytes');
		await assert.rejects(
			checkPublished({
				actionlint: process.execPath,
				fixture: 'unused.ts',
				receipt: artifact,
				report: artifact,
			}),
			/overwrite an input/,
		);
		assert.equal(await readFile(artifact, 'utf8'), 'candidate bytes');
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

test('failed verification removes an old success receipt', async () => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-stale-receipt-'));
	try {
		const report = join(directory, 'candidate.json');
		await writeFile(report, '{"offline":true}');
		await assert.rejects(
			checkCandidate({
				actionlint: join(directory, 'missing-host'),
				fixture: 'unused.ts',
				registry: 'unused',
				artifact: 'unused.wasm',
				store: directory,
				report,
			}),
			/ENOENT/,
		);
		await assert.rejects(readFile(report), /ENOENT/);
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

test('failed delivery preflight removes an old delivery report', async () => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-delivery-report-'));
	try {
		const receipt = join(directory, 'candidate.json');
		const report = join(directory, 'installation.json');
		await writeFile(receipt, '{}');
		await writeFile(report, '{"offline":true}');
		await assert.rejects(
			checkPublished({ actionlint: 'unused', fixture: 'unused.ts', receipt, report }),
			/successful candidate receipt/,
		);
		await assert.rejects(readFile(report), /ENOENT/);
		assert.equal(await readFile(receipt, 'utf8'), '{}');
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

test('fixture processes receive a fresh home and no inherited credentials', async () => {
	const directory = await mkdtemp(join(tmpdir(), 'registry-environment-'));
	try {
		const environment = cleanEnvironment(directory, join(directory, 'cache'));
		const output = await execute(
			process.execPath,
			['-e', 'console.log(JSON.stringify(process.env))'],
			directory,
			environment,
		);
		const actual: unknown = JSON.parse(output);
		assert.ok(actual && typeof actual === 'object');
		assert.equal('GH_TOKEN' in actual, false);
		assert.equal('ACTIONS_ID_TOKEN_REQUEST_TOKEN' in actual, false);
		assert.equal('HOME' in actual && actual.HOME, directory);
		assert.equal(
			'HTTPS_PROXY' in actual && actual.HTTPS_PROXY,
			'http://127.0.0.1:1',
		);
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
});

test('guest output cannot grow the check process without a bound', async () => {
	await assert.rejects(
		execute(
			process.execPath,
			['-e', 'process.stdout.write("x".repeat(3 * 1024 * 1024))'],
			process.cwd(),
			process.env,
		),
		/exceeded 2 MiB/,
	);
});
