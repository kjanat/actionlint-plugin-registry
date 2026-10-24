import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { executeResult } from '@/scripts/process.ts';

const root = fileURLToPath(new URL('../', import.meta.url));

for (const name of ['claim', 'complete', 'recover']) {
	test(`consume ${name} help includes the runnable command path`, async () => {
		const { stdout, stderr } = await executeResult(
			process.execPath,
			['scripts/cli.ts', 'consume', name, '--help'],
			root,
			{ ...process.env, NO_COLOR: '1' },
		);
		assert.equal(stdout.split('\n')[0]?.trim(), `Usage: actionlint-registry consume ${name} [flags]`);
		assert.equal(stderr, '');
	});
}
