import { appendFile } from 'node:fs/promises';

const maxResponse = 1024 * 1024;
const registryOrigin = 'https://registry.actionlint.kjanat.dev';

const errorReasons: Record<string, readonly [number, string]> = {
	oidc_invalid: [401, 'OIDC token invalid or expired'],
	oidc_bearer_missing: [401, 'OIDC bearer credential missing or invalid'],
	github_issuer: [403, 'GitHub issuer required'],
	github_visibility: [403, 'public GitHub repository required'],
	github_subject: [403, 'GitHub subject claim missing or invalid'],
	github_repository_id: [403, 'GitHub repository ID claim missing or invalid'],
	github_owner_id: [403, 'GitHub owner ID claim missing or invalid'],
	github_repository: [403, 'GitHub repository claim missing or invalid'],
	github_ref: [403, 'GitHub ref claim missing or invalid'],
	github_commit: [403, 'GitHub commit claim missing or invalid'],
	github_workflow: [403, 'GitHub workflow claim missing or invalid'],
	github_run_id: [403, 'GitHub run ID claim missing or invalid'],
	github_run_attempt: [403, 'GitHub run attempt claim missing or invalid'],
	validator_repository: [403, 'validator repository ID mismatch'],
	validator_workflow: [403, 'validator workflow reference mismatch'],
	validator_ref: [403, 'validator branch reference mismatch'],
	validator_ref_type: [403, 'validator reference type must be branch'],
	validator_event: [
		403,
		'validator event must be schedule or workflow_dispatch',
	],
};

export function parseJSON(source: string): unknown {
	const value: unknown = JSON.parse(source);
	const stack: ({ keys: Set<string>; key: boolean } | null)[] = [];
	for (const match of source.matchAll(/"(?:\\[\s\S]|[^"\\])*"|[{}[\],:]/g)) {
		const token = match[0];
		if (token === '{') stack.push({ keys: new Set(), key: true });
		else if (token === '[') stack.push(null);
		else if (token === '}' || token === ']') stack.pop();
		else {
			const parent = stack.at(-1);
			if (!parent) continue;
			if (token === ',') parent.key = true;
			else if (token === ':') parent.key = false;
			else if (parent.key) {
				const name: unknown = JSON.parse(token);
				if (typeof name !== 'string' || parent.keys.has(name)) {
					throw new Error('Duplicate JSON member');
				}
				parent.keys.add(name);
			}
		}
	}
	return value;
}

export function registryErrorReason(status: number, body: string): string {
	try {
		const value = parseJSON(body);
		if (
			value
			&& typeof value === 'object'
			&& 'code' in value
			&& typeof value.code === 'string'
		) {
			const reason = errorReasons[value.code];
			if (reason?.[0] === status) return reason[1];
		}
	} catch {
		/* Unrecognized error bodies do not become operator-facing diagnostics. */
	}
	return 'no recognized registry error code';
}

async function boundedBody(response: Response, limit: number): Promise<string> {
	let size = 0;
	const chunks: Uint8Array[] = [];
	if (response.body) {
		const reader = response.body.getReader();
		try {
			for (;;) {
				const chunk = await reader.read().catch(() => {
					throw new TransportError('HTTP response interrupted');
				});
				if (chunk.done) break;
				size += chunk.value.length;
				if (size > limit) {
					await reader.cancel();
					throw new Error('HTTP response exceeds limit');
				}
				chunks.push(chunk.value);
			}
		} finally {
			reader.releaseLock();
		}
	}
	return Buffer.concat(chunks).toString('utf8');
}

export class HTTPError extends Error {
	readonly status: number;
	readonly retryAfter: number;
	constructor(status: number, retryAfter = 0, reason = '') {
		super(
			`HTTP request failed with status ${status}${reason ? `: ${reason}` : ''}`,
		);
		this.status = status;
		this.retryAfter = retryAfter;
	}
}

export class TransportError extends Error {}

export class HTTPClient {
	async response(url: string, init: RequestInit = {}): Promise<Response> {
		try {
			const response = await fetch(url, {
				...init,
				redirect: 'error',
				signal: AbortSignal.timeout(30_000),
				headers: { 'User-Agent': 'actionlint-registry/1', ...init.headers },
			});
			if (!response.ok) {
				const retry = response.headers.get('retry-after');
				const seconds = retry === null ? 0 : Number(retry);
				const date = retry === null ? 0 : Date.parse(retry);
				const delay = Number.isFinite(seconds)
					? seconds * 1000
					: Math.max(0, date - Date.now());
				let reason = '';
				if (new URL(url).origin === registryOrigin) {
					try {
						reason = registryErrorReason(
							response.status,
							await boundedBody(response, 8192),
						);
					} catch {
						reason = 'no recognized registry error code';
					}
				} else await response.body?.cancel();
				throw new HTTPError(
					response.status,
					Number.isFinite(delay) ? delay : 0,
					reason,
				);
			}
			return response;
		} catch (error) {
			if (error instanceof HTTPError) throw error;
			throw new TransportError('HTTP request failed');
		}
	}

	async json(
		url: string,
		init: RequestInit = {},
	): Promise<Record<string, unknown>> {
		const response = await this.response(url, init);
		const source = await boundedBody(response, maxResponse);
		try {
			const value = parseJSON(source);
			if (typeof value !== 'object' || value === null || Array.isArray(value)) {
				throw new Error('Expected JSON object');
			}
			return Object.fromEntries(Object.entries(value));
		} catch {
			throw new Error('HTTP response must be a JSON object');
		}
	}
}

export function uuidValue(value: unknown): string {
	if (
		typeof value !== 'string'
		|| !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(
			value,
		)
	) {
		throw new Error('Invalid submission or lease identifier');
	}
	return value;
}

export async function output(name: string, value: string): Promise<void> {
	if (!/^[a-z][a-z0-9-]*$/.test(name) || /[\r\n]/.test(value)) {
		throw new Error('Invalid workflow output');
	}
	if (process.env.GITHUB_OUTPUT) {
		await appendFile(process.env.GITHUB_OUTPUT, `${name}=${value}\n`);
	}
}

export async function requestJSON(
	url: string,
	token: string,
	payload?: unknown,
	operation = 'Registry request',
	http = new HTTPClient(),
): Promise<Record<string, unknown>> {
	let parsed: URL;
	try {
		parsed = new URL(url);
	} catch {
		throw new Error('Invalid HTTPS endpoint');
	}
	if (
		parsed.protocol !== 'https:'
		|| parsed.username
		|| parsed.password
		|| parsed.hash
	) {
		throw new Error('HTTPS endpoint without credentials required');
	}
	if (!/^[A-Za-z0-9._~+/-]+=*$/.test(token) || token.length > 16384) {
		throw new Error('Invalid credential format');
	}
	try {
		const request: RequestInit = {
			headers: {
				Authorization: `Bearer ${token}`,
				Accept: 'application/json',
				'Content-Type': 'application/json',
			},
		};
		if (payload !== undefined) {
			request.method = 'POST';
			request.body = JSON.stringify(payload);
		}
		return await http.json(url, request);
	} catch (error) {
		if (error instanceof HTTPError || error instanceof TransportError) {
			throw new Error(`${operation}: ${error.message}`);
		}
		throw error;
	}
}

export async function oidcToken(
	audience: string,
	environ: NodeJS.ProcessEnv,
	provider = 'github',
	http = new HTTPClient(),
): Promise<string> {
	let token: unknown;
	if (provider === 'gitlab') {
		token = environ.REGISTRY_ID_TOKEN;
	} else if (provider === 'github') {
		if (!environ.ACTIONS_ID_TOKEN_REQUEST_URL) {
			throw new Error(
				'GitHub OIDC endpoint missing; grant the job id-token: write',
			);
		}
		let endpoint: URL;
		try {
			endpoint = new URL(environ.ACTIONS_ID_TOKEN_REQUEST_URL);
		} catch {
			throw new Error('GitHub OIDC endpoint invalid or unsupported');
		}
		if (
			endpoint.protocol !== 'https:'
			|| !endpoint.hostname.endsWith('.actions.githubusercontent.com')
			|| endpoint.username
			|| endpoint.password
			|| endpoint.hash
			|| (endpoint.port && endpoint.port !== '443')
		) {
			throw new Error('GitHub OIDC endpoint invalid or unsupported');
		}
		endpoint.searchParams.set('audience', audience);
		const credential = environ.ACTIONS_ID_TOKEN_REQUEST_TOKEN ?? '';
		if (
			/^[A-Za-z0-9._~+/-]+=*$/.test(credential)
			&& environ.GITHUB_ACTIONS === 'true'
		) {
			console.log(`::add-mask::${credential}`);
		}
		const identity = await requestJSON(
			endpoint.href,
			credential,
			undefined,
			'GitHub OIDC token request',
			http,
		);
		token = identity.value;
	} else {
		throw new Error('Unsupported identity provider');
	}
	if (
		typeof token !== 'string'
		|| token.length > 16384
		|| !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(token)
	) {
		throw new Error('Invalid OIDC token');
	}
	if (environ.GITHUB_ACTIONS === 'true') console.log(`::add-mask::${token}`);
	return token;
}
