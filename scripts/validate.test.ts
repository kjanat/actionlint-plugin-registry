import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

test('validator logs cannot execute workflow commands, and logging resumes after either exit status', () => {
	// Guest output may contain GitHub runner command syntax. Execute the real shell wrapper to
	// check it disables interpretation, restores it after failures, and uses a fresh secret token.
	const tokens: string[] = [];
	for (const status of [0, 7]) {
		const result = spawnSync(
			'bash',
			[
				fileURLToPath(new URL('./validate.sh', import.meta.url)).replaceAll(
					'\\',
					'/',
				),
				'bash',
				'-c',
				'printf "%s\\n" "::warning::guest text"; exit "$1"',
				'test',
				String(status),
			],
			{ encoding: 'utf8', timeout: 15_000 },
		);
		assert.equal(result.status, status, result.stderr);
		const lines = result.stdout.trimEnd().split(/\r?\n/);
		assert.equal(lines.length, 3);
		const match = /^::stop-commands::([0-9a-f]{64})$/.exec(lines[0] ?? '');
		assert.ok(match?.[1]);
		tokens.push(match[1]);
		assert.equal(lines[1], '::warning::guest text');
		assert.equal(lines[2], `::${match[1]}::`);
	}
	assert.notEqual(tokens[0], tokens[1]);
});
