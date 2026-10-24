import { spawn } from 'node:child_process';

export function object(value: unknown): Record<string, unknown> {
	if (typeof value !== 'object' || value === null || Array.isArray(value)) {
		throw new Error('Expected a JSON object');
	}
	return Object.fromEntries(Object.entries(value));
}

export function strings(value: unknown): string[] {
	if (!Array.isArray(value)) throw new Error('Expected a string array');
	return value.map((item: unknown) => {
		if (typeof item !== 'string' || item.length === 0) {
			throw new Error('Expected a nonempty string');
		}
		return item;
	});
}

export function cleanEnvironment(
	home: string,
	cache: string,
): NodeJS.ProcessEnv {
	const result: NodeJS.ProcessEnv = {};
	for (
		const name of [
			'PATH',
			'SystemRoot',
			'COMSPEC',
			'PATHEXT',
			'LANG',
			'LC_ALL',
		]
	) {
		if (process.env[name] !== undefined) result[name] = process.env[name];
	}
	return {
		...result,
		HOME: home,
		USERPROFILE: home,
		XDG_CONFIG_HOME: home,
		APPDATA: home,
		XDG_CACHE_HOME: cache,
		LOCALAPPDATA: cache,
		TMPDIR: home,
		TMP: home,
		TEMP: home,
		HTTP_PROXY: 'http://127.0.0.1:1',
		HTTPS_PROXY: 'http://127.0.0.1:1',
		ALL_PROXY: 'http://127.0.0.1:1',
		NO_PROXY: '',
	};
}

export async function execute(
	binary: string,
	args: string[],
	directory: string,
	env: NodeJS.ProcessEnv,
	expected: number | number[] = 0,
): Promise<string> {
	return (await executeResult(binary, args, directory, env, expected)).stdout;
}

export async function executeResult(
	binary: string,
	args: string[],
	directory: string,
	env: NodeJS.ProcessEnv,
	expected: number | number[] = 0,
): Promise<{ stdout: string; stderr: string }> {
	return new Promise((resolve, reject) => {
		const child = spawn(binary, args, {
			cwd: directory,
			env,
			shell: false,
			windowsHide: true,
			stdio: ['ignore', 'pipe', 'pipe'],
			timeout: 120_000,
		});
		const stdout: Buffer[] = [];
		const stderr: Buffer[] = [];
		let size = 0;
		let overflow = false;
		const capture = (chunks: Buffer[]) => (chunk: Buffer) => {
			size += chunk.length;
			if (size > 2 * 1024 * 1024) {
				overflow = true;
				child.kill();
				return;
			}
			chunks.push(chunk);
		};
		child.stdout.on('data', capture(stdout));
		child.stderr.on('data', capture(stderr));
		child.on('error', reject);
		child.on('close', (code, signal) => {
			if (overflow) {
				reject(new Error('Command output exceeded 2 MiB'));
				return;
			}
			if (
				code === null
				|| !(Array.isArray(expected) ? expected : [expected]).includes(code)
				|| signal !== null
			) {
				reject(
					new Error(
						`${binary} ${args.join(' ')}: exit ${code}, expected ${expected}\n${Buffer.concat(stdout)}\n${
							Buffer.concat(stderr)
						}`,
					),
				);
				return;
			}
			resolve({
				stdout: Buffer.concat(stdout).toString('utf8'),
				stderr: Buffer.concat(stderr).toString('utf8'),
			});
		});
	});
}
