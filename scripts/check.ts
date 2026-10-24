import { createHash } from 'node:crypto';
import { chmod, copyFile, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { cleanEnvironment, execute, executeResult, object, strings } from '@/scripts/process.ts';

interface InstallationOptions {
	actionlint: string;
	fixture: string;
	report: string;
}
export interface CheckOptions extends InstallationOptions {
	registry: string;
	artifact: string;
	store: string;
}
export interface PublishedOptions extends InstallationOptions {
	receipt: string;
	tag: string;
}
type Source =
	| { kind: 'candidate'; options: CheckOptions }
	| { kind: 'published'; artifact: Record<string, unknown>; version: string };

function sha256(data: Buffer): string {
	return createHash('sha256').update(data).digest('hex');
}
function string(value: unknown, name: string): string {
	if (typeof value !== 'string' || value.length === 0) {
		throw new Error(`Missing ${name}`);
	}
	return value;
}

async function cachedArtifacts(
	directory: string,
	digest: string,
): Promise<string[]> {
	const found: string[] = [];
	for (const item of await readdir(directory, { withFileTypes: true })) {
		const path = join(directory, item.name);
		if (item.isDirectory()) {
			found.push(...(await cachedArtifacts(path, digest)));
		} else if (
			item.isFile()
			&& item.name === digest
			&& sha256(await readFile(path)) === digest
		) {
			found.push(path);
		}
	}
	return found;
}

export async function checkCandidate(options: CheckOptions) {
	await prepareReport(options, [options.artifact, options.registry]);
	return verifyInstallation(options, { kind: 'candidate', options });
}

export async function checkPublished(options: PublishedOptions, run = execute) {
	await prepareReport(options, [options.receipt]);
	const receipt = object(JSON.parse(await readFile(options.receipt, 'utf8')));
	if (
		receipt.schema_version !== 1
		|| receipt.offline !== true
		|| receipt.missing_cache_rejected !== true
	) {
		throw new Error('A successful candidate receipt is required');
	}
	if (
		object(receipt.fixture).sha256 !== sha256(await readFile(options.fixture))
	) {
		throw new Error('Delivery fixture differs from the tested candidate');
	}
	if (object(receipt.host).sha256 !== sha256(await readFile(options.actionlint))) {
		throw new Error('Delivery host differs from the tested candidate');
	}
	const artifact = object(receipt.artifact);
	if (
		!/^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/.test(options.tag)
		|| options.tag.replace(/^v/u, '') !== string(artifact.plugin_version, 'version').replace(/^v/u, '')
	) {
		throw new Error('Delivery release tag differs from the tested candidate');
	}
	return verifyInstallation(options, {
		kind: 'published',
		artifact,
		version: options.tag,
	}, run);
}

async function prepareReport(options: InstallationOptions, extraInputs: string[]) {
	const inputs = [options.actionlint, options.fixture, ...extraInputs];
	if (
		inputs.some(
			(input) => resolve(input).toLowerCase() === resolve(options.report).toLowerCase(),
		)
	) {
		throw new Error('Report must not overwrite an input');
	}
	await rm(resolve(options.report), { force: true });
}

async function verifyInstallation(
	options: InstallationOptions,
	source: Source,
	run = execute,
) {
	const root = await mkdtemp(join(tmpdir(), 'registry-candidate-'));
	try {
		const project = join(root, 'project');
		const cache = join(root, 'cache');
		const home = join(root, 'home');
		for (const directory of [project, cache, home, join(project, '.git')]) {
			await mkdir(directory);
		}
		const env = cleanEnvironment(home, cache);
		const catalogue = join(root, 'catalogue');
		const binary = join(
			root,
			process.platform === 'win32' ? 'actionlint.exe' : 'actionlint',
		);
		await copyFile(resolve(options.actionlint), binary);
		await chmod(binary, 0o700);
		const hostDigest = sha256(await readFile(binary));
		const hostVersion: unknown = JSON.parse(
			await run(binary, ['version', '--json'], project, env),
		);
		const fixture = resolve(options.fixture);
		const fixtureDigest = sha256(await readFile(fixture));
		const cli = fileURLToPath(new URL('./cli.ts', import.meta.url));
		await run(
			process.execPath,
			[
				cli,
				'fixture',
				'--fixture',
				fixture,
				'--actionlint',
				binary,
				'--directory',
				project,
				'--phase',
				'prepare',
			],
			project,
			env,
		);
		const artifact = source.kind === 'published'
			? source.artifact
			: object(
				object(
					JSON.parse(
						await run(
							resolve(source.options.registry),
							[
								'candidate',
								'--artifact',
								resolve(source.options.artifact),
								'--store',
								resolve(source.options.store),
								'--output',
								catalogue,
							],
							project,
							env,
						),
					),
				).artifact,
			);
		const digest = string(artifact.sha256, 'artifact digest');
		const name = string(artifact.repository, 'repository').replace(
			/^https:\/\//u,
			'',
		);
		const version = source.kind === 'published'
			? source.version
			: `v${string(artifact.plugin_version, 'version').replace(/^v/u, '')}`;
		const install = ['plugin', 'install', name, '--version', version, '--json'];
		const installEnvironment = { ...env };
		if (source.kind === 'candidate') {
			install.push('--registry', `${pathToFileURL(catalogue).href}/`);
		} else {
			for (const proxy of ['HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY']) {
				delete installEnvironment[proxy];
			}
		}
		await run(binary, install, project, installEnvironment);
		const lockPath = join(project, '.github', 'actionlint.plugins.lock.json');
		const lock = await readFile(lockPath);
		const plugins = object(JSON.parse(lock.toString('utf8'))).plugins;
		if (!Array.isArray(plugins) || plugins.length !== 1) {
			throw new Error('Installer must lock exactly one candidate');
		}
		const pin = object(plugins[0]);
		if (
			(source.kind === 'candidate'
				? pin.source !== 'candidate'
				: pin.source === 'candidate')
			|| pin.sha256 !== digest
			|| pin.version !== version
			|| string(pin.name, 'installed repository').toLowerCase() !== name.toLowerCase()
			|| pin.contract !== artifact.contract
			|| pin.plugin_id !== artifact.plugin_id
		) {
			throw new Error('Installed plugin differs from inspected package');
		}
		const probeFixture = async () =>
			object(
				JSON.parse(
					await run(
						process.execPath,
						[
							cli,
							'fixture',
							'--fixture',
							fixture,
							'--actionlint',
							binary,
							'--directory',
							project,
							'--phase',
							'verify',
						],
						project,
						env,
					),
				),
			);
		const first = await probeFixture();
		const checks = strings(first.checks);
		const probe = strings(first.probe);
		const positive = object(
			JSON.parse(await run(binary, probe, project, env, [0, 1])),
		);
		if (
			positive.completed !== true
			|| (positive.exit_code !== 0 && positive.exit_code !== 1)
			|| !Array.isArray(positive.diagnostics)
		) {
			throw new Error(
				'Fixture probe did not complete analysis before cache removal',
			);
		}
		if (source.kind === 'candidate') await rm(catalogue, { recursive: true });
		const offline = await probeFixture();
		if (JSON.stringify(offline) !== JSON.stringify(first)) {
			throw new Error('Offline fixture results changed');
		}
		if (!(await readFile(lockPath)).equals(lock)) {
			throw new Error('Checking modified the lockfile');
		}
		const cached = await cachedArtifacts(cache, digest);
		const [cachedArtifact] = cached;
		if (cached.length !== 1 || cachedArtifact === undefined) {
			throw new Error(
				'Expected exactly one cached candidate with the inspected digest',
			);
		}
		await rm(cachedArtifact);
		const missing = await executeResult(binary, probe, project, env, 3);
		if (
			missing.stdout.trim() !== ''
			|| !missing.stderr.toLowerCase().includes('plugin')
		) {
			throw new Error('Missing cache did not report a plugin error');
		}
		const receipt = {
			schema_version: 1,
			artifact: { ...artifact, sha256: digest },
			host: { sha256: hostDigest, version: hostVersion },
			fixture: { sha256: fixtureDigest, checks },
			installation: { version, sha256: digest },
			offline: true,
			missing_cache_rejected: true,
		};
		if (sha256(await readFile(fixture)) !== fixtureDigest) {
			throw new Error('Fixture changed during verification');
		}
		await writeFile(
			resolve(options.report),
			`${JSON.stringify(receipt, null, 2)}\n`,
		);
		return receipt;
	} finally {
		await rm(root, { recursive: true, force: true });
	}
}
