export class HttpError extends Error {
	constructor(
		readonly status: number,
		message: string,
	) {
		super(message);
	}
}

export function object(value: unknown): Record<string, unknown> {
	if (typeof value !== 'object' || value === null || Array.isArray(value)) {
		throw new HttpError(400, 'The value must be a JSON object.');
	}
	return Object.fromEntries(Object.entries(value));
}

export function text(value: unknown, limit = 512): string {
	if (
		typeof value !== 'string'
		|| value.length === 0
		|| value.length > limit
	) {
		throw new HttpError(400, 'The value must be a nonempty string within the permitted length.');
	}
	for (const character of value) {
		const code = character.charCodeAt(0);
		if (code <= 0x1f || code === 0x7f) {
			throw new HttpError(400, 'The value must not contain ASCII control characters.');
		}
	}
	return value;
}

export function exactKeys(
	value: Record<string, unknown>,
	keys: string[],
): void {
	if (Object.keys(value).some((key) => !keys.includes(key))) {
		throw new HttpError(400, 'The request contains an unsupported field.');
	}
}

export interface Submission {
	schema_version: 1;
	provider: 'github' | 'gitlab';
	repository: string;
	tag: string;
	asset: string;
	sha256: string;
	kind: 'core' | 'component';
}

export function submission(value: unknown): Submission {
	const v = object(value);
	exactKeys(v, [
		'schema_version',
		'provider',
		'repository',
		'tag',
		'asset',
		'sha256',
		'kind',
	]);
	if (
		v.schema_version !== 1
		|| (v.provider !== 'github' && v.provider !== 'gitlab')
		|| (v.kind !== 'core' && v.kind !== 'component')
	) {
		throw new HttpError(
			400,
			'The submission specifies an unsupported schema version, provider, or plugin format.',
		);
	}
	const repository = text(v.repository, 256).toLowerCase();
	const parts = repository.split('/');
	if (
		parts.length < 2
		|| (v.provider === 'github' && parts.length !== 2)
		|| parts.some((p) => !/^[a-z0-9][a-z0-9._-]*$/u.test(p) || p.endsWith('.git'))
	) {
		throw new HttpError(400, 'The repository path is invalid for the selected provider.');
	}
	const tag = text(v.tag, 128);
	const asset = text(v.asset, 128);
	const sha256 = text(v.sha256, 64);
	if (
		!/^[A-Za-z0-9][A-Za-z0-9._+-]*$/u.test(tag)
		|| !/^[A-Za-z0-9][A-Za-z0-9._+-]*\.wasm(?:\.gz)?$/u.test(asset)
		|| !/^[a-f0-9]{64}$/u.test(sha256)
	) {
		throw new HttpError(400, 'The release tag, asset filename, or SHA-256 checksum is invalid.');
	}
	return {
		schema_version: 1,
		provider: v.provider,
		repository,
		tag,
		asset,
		sha256,
		kind: v.kind,
	};
}

export interface Identity {
	issuer: string;
	subject: string;
	repository_id: string;
	owner_id: string;
	repository: string;
	ref: string;
	commit: string;
	workflow: string;
	run_id: string;
}

export interface Receipt {
	schema_version: 1;
	id: string;
	submission: Submission;
	identity: Identity;
	authorized_at: string;
}

export interface Authorization {
	identity: Identity;
	jti: string;
	expires: number;
}

export async function body(request: Request): Promise<unknown> {
	if (
		request.headers.get('content-type')?.split(';')[0]?.trim()
			!== 'application/json'
	) {
		throw new HttpError(415, 'Set the Content-Type header to application/json.');
	}
	if (request.body === null) throw new HttpError(400, 'The request must include a JSON body.');
	const reader = request.body.getReader();
	const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: false });
	let size = 0;
	let result = '';
	try {
		for (;;) {
			const chunk = await reader.read();
			if (chunk.done) break;
			size += chunk.value.byteLength;
			if (size > 16384) {
				await reader.cancel();
				throw new HttpError(413, 'The request body exceeds 16 KiB.');
			}
			result += decoder.decode(chunk.value, { stream: true });
		}
		result += decoder.decode();
		return JSON.parse(result);
	} catch (error) {
		if (error instanceof HttpError) throw error;
		throw new HttpError(400, 'The request body is not valid JSON.');
	} finally {
		reader.releaseLock();
	}
}

export async function digest(value: string): Promise<string> {
	const bytes = await crypto.subtle.digest(
		'SHA-256',
		new TextEncoder().encode(value),
	);
	return Array.from(new Uint8Array(bytes), (n) => n.toString(16).padStart(2, '0')).join('');
}
