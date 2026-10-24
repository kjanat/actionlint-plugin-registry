import { pathToFileURL } from 'node:url';
import { execute, executeResult, object, strings } from '@/scripts/process.ts';

// Trusted publisher fixtures run in child processes with the host's fresh home
// and credential-free environment.
export async function runFixture(
	fixture: string,
	host: string,
	directory: string,
	phase: 'prepare' | 'verify',
): Promise<void> {
	const loaded: unknown = await import(pathToFileURL(fixture).href);
	const module = object(loaded);
	if (phase === 'prepare') {
		if (typeof module.prepare !== 'function') {
			throw new Error('Fixture must export prepare({directory})');
		}
		await module.prepare({ directory });
		return;
	}
	if (typeof module.verify !== 'function') {
		throw new Error('Fixture must export verify({directory, run})');
	}
	const result: unknown = await module.verify({
		directory,
		run: (args: string[], expected = 0) => execute(host, args, directory, process.env, expected),
		runResult: (args: string[], expected = 0) => executeResult(host, args, directory, process.env, expected),
	});
	const checked = object(result);
	const checks = strings(checked.checks);
	const probe = strings(checked.probe);
	if (checks.length === 0 || probe[0] !== 'check' || !probe.includes('--json')) {
		throw new Error('Fixture must return checks and a check --json probe');
	}
	process.stdout.write(`${JSON.stringify({ checks, probe })}\n`);
}
