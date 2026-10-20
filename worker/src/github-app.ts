import { importPKCS8, SignJWT } from 'jose';

export interface GitHubAppConfig {
	GITHUB_APP_CLIENT_ID: string;
	GITHUB_APP_INSTALLATION_ID: string;
	GITHUB_APP_PRIVATE_KEY: string;
	REGISTRY_REPOSITORY_ID: string;
}

export interface DispatchResult {
	runID: string | null;
}

export type GitHubFetch = (request: Request) => Promise<Response>;

type RequestStage = 'installation-token' | 'workflow-dispatch';
type DispatchStage = RequestStage | 'configuration' | 'signing' | 'installation-grant' | 'dispatch-response';

type DispatchFailureDetails =
	| { code: 'invalid'; stage: DispatchStage }
	| {
		code: 'invalid';
		stage: 'installation-grant';
		reason: 'permissions' | 'repositories-count' | 'repository-identity' | 'token-format' | 'expiry';
	}
	| { code: 'request-failed'; stage: RequestStage }
	| { code: 'http-error'; stage: RequestStage; status: number };

/** Only locally constructed classifications survive; never retain upstream errors or their causes. */
export class DispatchFailure extends Error {
	constructor(readonly details: DispatchFailureDetails) {
		super('The GitHub App could not dispatch registry validation.');
		this.name = 'DispatchFailure';
	}
}

const repository = 'kjanat/actionlint-registry';
const repositoryID = '1424600475';
const api = 'https://api.github.com';
const maxResponse = 65536;

function derLength(length: number): number[] {
	if (length < 128) return [length];
	const bytes: number[] = [];
	for (let remaining = length; remaining > 0; remaining = Math.floor(remaining / 256)) {
		bytes.unshift(remaining % 256);
	}
	return [128 + bytes.length, ...bytes];
}

/** GitHub downloads PKCS#1 keys, which Web Crypto requires inside a PKCS#8 envelope. */
function pkcs8(pem: string): string {
	if (pem.length > 16384) throw new Error('The private key is too large.');
	const match = /^-----BEGIN (RSA PRIVATE KEY|PRIVATE KEY)-----\s+([A-Za-z0-9+/=\s]+)-----END \1-----$/u.exec(
		pem.trim(),
	);
	if (!match || match[1] === undefined || match[2] === undefined) {
		throw new Error('The private key format is invalid.');
	}
	const encoded = match[2].replace(/\s/gu, '');
	const decoded = atob(encoded);
	if (btoa(decoded) !== encoded) throw new Error('The private key encoding is invalid.');
	if (match[1] === 'PRIVATE KEY') return pem.trim();
	// PrivateKeyInfo contains version zero, rsaEncryption with NULL parameters, and the key octets.
	const key = Array.from(decoded, character => character.charCodeAt(0));
	const contents = /* dprint-ignore */ [
    2, 1, 0, 48, 13, 6, 9, 42, 134, 72, 134, 247, 13, 1, 1, 1, 5, 0, 4,
		...derLength(key.length),
		...key,
	];
	const wrapped = [48, ...derLength(contents.length), ...contents];
	const base64 = btoa(String.fromCharCode(...wrapped));
	return `-----BEGIN PRIVATE KEY-----\n${base64}\n-----END PRIVATE KEY-----`;
}

function record(value: unknown): Record<string, unknown> {
	if (value === null || typeof value !== 'object' || Array.isArray(value)) {
		throw new Error('GitHub returned an invalid response.');
	}
	return Object.fromEntries(Object.entries(value));
}

async function responseJSON(response: Response): Promise<Record<string, unknown>> {
	if (response.body === null) throw new Error('GitHub returned an empty response.');
	const reader = response.body.getReader();
	const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: false });
	let length = 0;
	let text = '';
	try {
		for (;;) {
			const chunk = await reader.read();
			if (chunk.done) break;
			length += chunk.value.byteLength;
			if (length > maxResponse) {
				await reader.cancel();
				throw new Error('GitHub returned an oversized response.');
			}
			text += decoder.decode(chunk.value, { stream: true });
		}
		return record(JSON.parse(text + decoder.decode()));
	} finally {
		reader.releaseLock();
	}
}

async function post(
	fetcher: GitHubFetch,
	stage: RequestStage,
	path: string,
	token: string,
	body: unknown,
): Promise<Response> {
	let response: Response;
	try {
		response = await fetcher(
			new Request(api + path, {
				method: 'POST',
				// Workerd supports manual/follow only. Reject non-2xx below;
				// never forward the installation token across a redirect.
				redirect: 'manual',
				signal: AbortSignal.timeout(10000),
				headers: {
					authorization: `Bearer ${token}`,
					accept: 'application/vnd.github+json',
					'content-type': 'application/json',
					'user-agent': 'actionlint-registry',
					'x-github-api-version': '2026-03-10',
				},
				body: JSON.stringify(body),
			}),
		);
	} catch {
		throw new DispatchFailure({ code: 'request-failed', stage });
	}
	if (!response.ok) {
		try {
			await response.body?.cancel();
		} catch {
			// Cancellation errors cannot replace the known HTTP failure or expose response details.
		}
		if (!Number.isInteger(response.status) || response.status < 100 || response.status > 599) {
			throw new DispatchFailure({ code: 'invalid', stage });
		}
		throw new DispatchFailure({ code: 'http-error', stage, status: response.status });
	}
	return response;
}

/** Dispatch only wakes the trusted consumer; the durable OIDC receipt remains its authority. */
export async function dispatchAdmission(
	config: GitHubAppConfig,
	fetcher: GitHubFetch = request => fetch(request),
): Promise<DispatchResult> {
	let stage: DispatchStage = 'configuration';
	try {
		if (
			!/^[A-Za-z0-9_.-]{1,128}$/u.test(config.GITHUB_APP_CLIENT_ID)
			|| !/^[1-9][0-9]{0,19}$/u.test(config.GITHUB_APP_INSTALLATION_ID)
			|| config.REGISTRY_REPOSITORY_ID !== repositoryID
		) throw new Error('The GitHub App configuration is invalid.');
		stage = 'signing';
		const now = Math.floor(Date.now() / 1000);
		const key = await importPKCS8(pkcs8(config.GITHUB_APP_PRIVATE_KEY), 'RS256');
		const jwt = await new SignJWT({})
			.setProtectedHeader({ alg: 'RS256', typ: 'JWT' })
			.setIssuer(config.GITHUB_APP_CLIENT_ID)
			.setIssuedAt(now - 60)
			.setExpirationTime(now + 300)
			.sign(key);
		const response = await post(
			fetcher,
			'installation-token',
			`/app/installations/${config.GITHUB_APP_INSTALLATION_ID}/access_tokens`,
			jwt,
			{
				repository_ids: [Number(repositoryID)],
				permissions: { actions: 'write' },
			},
		);
		stage = 'installation-token';
		const grant = await responseJSON(response);
		stage = 'installation-grant';
		const permissions = record(grant.permissions);
		if (
			permissions.actions !== 'write'
			|| Object.entries(permissions).some(([name, value]) =>
				name !== 'actions' && (name !== 'metadata' || value !== 'read')
			)
		) throw new DispatchFailure({ code: 'invalid', stage, reason: 'permissions' });
		if (!Array.isArray(grant.repositories) || grant.repositories.length !== 1) {
			throw new DispatchFailure({ code: 'invalid', stage, reason: 'repositories-count' });
		}
		const grantedRepository = record(grant.repositories[0]);
		if (grantedRepository.id !== Number(repositoryID) || grantedRepository.full_name !== repository) {
			throw new DispatchFailure({ code: 'invalid', stage, reason: 'repository-identity' });
		}
		// Opaque bearer credentials: GitHub's stateless tokens exceed the old
		// length and contain JWT punctuation. Validate transport safety only.
		if (
			typeof grant.token !== 'string' || grant.token.length > 8192
			|| !/^[A-Za-z0-9._~+/-]+=*$/u.test(grant.token)
		) {
			throw new DispatchFailure({ code: 'invalid', stage, reason: 'token-format' });
		}
		if (
			typeof grant.expires_at !== 'string' || !Number.isFinite(Date.parse(grant.expires_at))
			|| Date.parse(grant.expires_at) <= Date.now() + 30000
		) throw new DispatchFailure({ code: 'invalid', stage, reason: 'expiry' });
		const dispatched = await post(
			fetcher,
			'workflow-dispatch',
			`/repos/${repository}/actions/workflows/admission.yml/dispatches`,
			grant.token,
			{ ref: 'master' },
		);
		stage = 'dispatch-response';
		// Earlier GitHub API versions returned 204 without a workflow run identity.
		if (dispatched.status === 204) return { runID: null };
		if (dispatched.status !== 200) {
			await dispatched.body?.cancel();
			throw new Error('GitHub returned an unexpected dispatch status.');
		}
		const result = await responseJSON(dispatched);
		if (
			typeof result.workflow_run_id !== 'number' || !Number.isSafeInteger(result.workflow_run_id)
			|| result.workflow_run_id <= 0
		) {
			throw new Error('GitHub returned an invalid workflow run identity.');
		}
		return { runID: String(result.workflow_run_id) };
	} catch (error) {
		// Upstream errors may contain credentials or signed request details.
		if (error instanceof DispatchFailure) throw error;
		throw new DispatchFailure({ code: 'invalid', stage });
	}
}
