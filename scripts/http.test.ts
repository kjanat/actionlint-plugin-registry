import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
	HTTPClient,
	HTTPError,
	oidcToken,
	parseJSON,
	registryErrorReason,
	requestJSON,
	TransportError,
} from '@/scripts/http.ts';
import { submit } from '@/scripts/publish.ts';

class RecordedClient extends HTTPClient {
	readonly calls: { url: string; init: RequestInit }[] = [];
	readonly responses: (Record<string, unknown> | Error)[];
	constructor(...responses: (Record<string, unknown> | Error)[]) {
		super();
		this.responses = responses;
	}
	override async json(
		url: string,
		init: RequestInit = {},
	): Promise<Record<string, unknown>> {
		this.calls.push({ url, init });
		const response = this.responses.shift();
		if (!response) throw new Error('Unexpected extra HTTP request');
		if (response instanceof Error) throw response;
		return response;
	}
}

test('interrupted response bodies are retryable, malformed and oversized JSON are not', async () => {
	// HTTP headers can arrive successfully before the connection fails mid-body. The waiter
	// must retry that interruption, but a complete invalid response needs an immediate error.
	class BodyClient extends HTTPClient {
		constructor(readonly body: Response) {
			super();
		}
		override async response(): Promise<Response> {
			return this.body;
		}
	}
	const interrupted = new Response(
		new ReadableStream<Uint8Array>({
			start(controller) {
				controller.enqueue(new TextEncoder().encode('{"status":'));
				controller.error(new TypeError('connection closed'));
			},
		}),
	);
	await assert.rejects(new BodyClient(interrupted).json('https://example.com'), TransportError);
	for (const body of ['{bad json', 'x'.repeat(1024 * 1024 + 1)]) {
		await assert.rejects(
			new BodyClient(new Response(body)).json('https://example.com'),
			(error: unknown) => error instanceof Error && !(error instanceof TransportError),
		);
	}
});

const token = 'header.payload.signature';
const origin = 'https://registry.actionlint.kjanat.dev';
const environment = {
	ACTIONS_ID_TOKEN_REQUEST_URL: 'https://run.actions.githubusercontent.com/token?audience=old&x=1',
	ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'request-token',
};
const identifier = '12345678-1234-4234-8234-123456789abc';

test('OIDC uses the registry audience and submits only release metadata', async () => {
	// Reusing GitHub's default audience would make valid publisher credentials fail admission.
	// Capture both requests to verify the audience, bearer boundary and exact submission body.
	const client = new RecordedClient(
		{ value: token },
		{ id: identifier, status: 'pending' },
	);
	assert.equal(
		await submit(
			{
				repository: 'Owner/Repo',
				tag: 'v1.0.0',
				asset: 'plugin.wasm',
				sha256: 'a'.repeat(64),
				kind: 'component',
			},
			environment,
			client,
		),
		identifier,
	);
	const [identity, submission] = client.calls;
	assert.ok(identity && submission);
	const url = new URL(identity.url);
	assert.deepEqual(url.searchParams.getAll('audience'), [origin]);
	assert.equal(url.searchParams.get('x'), '1');
	assert.equal(
		new Headers(identity.init.headers).get('authorization'),
		'Bearer request-token',
	);
	assert.equal(submission.url, `${origin}/v1/submissions`);
	assert.equal(
		new Headers(submission.init.headers).get('authorization'),
		`Bearer ${token}`,
	);
	assert.equal(typeof submission.init.body, 'string');
	assert.deepEqual(JSON.parse(String(submission.init.body)), {
		schema_version: 1,
		provider: 'github',
		repository: 'owner/repo',
		tag: 'v1.0.0',
		asset: 'plugin.wasm',
		sha256: 'a'.repeat(64),
		kind: 'component',
	});
});

test('unsafe endpoints and release names fail before credentials leave the process', async () => {
	// Untrusted endpoints must never receive runner credentials, and malformed release metadata
	// must fail before obtaining a token. Check the transport stays unused for each rejected input.
	const client = new RecordedClient();
	for (
		const endpoint of [
			'',
			'https://evil.test/token',
			'http://run.actions.githubusercontent.com/token',
			'https://run.actions.githubusercontent.com:bad/secret',
			'https://user:pass@run.actions.githubusercontent.com/token',
		]
	) {
		await assert.rejects(
			oidcToken(
				origin,
				{ ...environment, ACTIONS_ID_TOKEN_REQUEST_URL: endpoint },
				'github',
				client,
			),
			/OIDC endpoint/,
		);
	}
	for (
		const endpoint of [
			'http://example.test',
			'https://user:pass@example.test',
			'https://example.test/#fragment',
		]
	) {
		await assert.rejects(
			requestJSON(endpoint, token, undefined, 'Registry request', client),
			/HTTPS endpoint/,
		);
	}
	const entry = {
		repository: 'owner/repo',
		tag: 'v1.0.0',
		asset: 'plugin.wasm',
		sha256: 'a'.repeat(64),
	};
	for (
		const change of [
			{ repository: 'owner/repo/extra' },
			{ tag: 'v1\n::warning::value' },
			{ asset: '../plugin.wasm' },
			{
				sha256: 'invalid',
			},
		]
	) {
		await assert.rejects(
			submit({ ...entry, ...change, kind: 'core' }, environment, client),
		);
	}
	assert.equal(client.calls.length, 0);
});

test('GitLab publisher identity does not request a GitHub token', async () => {
	// Nested GitLab projects use the existing GitLab OIDC token. Accidentally taking the GitHub
	// token path would break publication outside GitHub and leak another platform's credential.
	const client = new RecordedClient({ id: identifier, status: 'published' });
	await submit(
		{
			repository: 'group/subgroup/project',
			tag: 'v1.0.0',
			asset: 'plugin.wasm.gz',
			sha256: 'b'.repeat(64),
			kind: 'core',
		},
		{ REGISTRY_ID_TOKEN: token },
		client,
		'gitlab',
	);
	assert.equal(client.calls.length, 1);
	assert.equal(client.calls[0]?.url, `${origin}/v1/submissions`);
});

test('submission terminal errors and unknown states cannot report success', async () => {
	// Registry acceptance is asynchronous, but rejected, failed and malformed responses must
	// already fail the submitting command. The ID must also be safe for workflow output files.
	for (
		const response of [
			{ id: identifier, status: 'rejected' },
			{ id: identifier, status: 'failed' },
			{
				id: identifier,
				status: 'other',
			},
			{ id: `${identifier}\nother-output=x`, status: 'pending' },
		]
	) {
		const client = new RecordedClient({ value: token }, response);
		await assert.rejects(
			submit(
				{
					repository: 'owner/repo',
					tag: 'v1',
					asset: 'plugin.wasm',
					sha256: 'a'.repeat(64),
					kind: 'core',
				},
				environment,
				client,
			),
		);
	}
});

test('HTTP diagnostics use closed labels instead of reflected credentials or response bodies', async () => {
	// Server error text can contain claims or credentials. Only recognized registry codes with
	// the correct status become readable labels; arbitrary, mismatched and duplicate codes do not.
	assert.equal(
		registryErrorReason(
			403,
			`{"code":"validator_workflow","error":"${token}"}`,
		),
		'validator workflow reference mismatch',
	);
	for (
		const body of [
			`{"code":"${token}"}`,
			`{"code":["validator_workflow"]}`,
			'{"code":"oidc_invalid"}',
			'{"code":"validator_workflow","code":"github_visibility"}',
			'<html>blocked</html>',
		]
	) {
		assert.equal(
			registryErrorReason(403, body),
			'no recognized registry error code',
		);
	}
	await assert.rejects(
		requestJSON(
			origin,
			token,
			undefined,
			'Registry claim',
			new RecordedClient(new HTTPError(403)),
		),
		{ message: 'Registry claim: HTTP request failed with status 403' },
	);
});

test('JSON duplicate names are rejected at every nesting level including escaped names', () => {
	// Different parsers can disagree about duplicate members, especially when escapes disguise
	// the repeated key. Reject these ambiguous responses while accepting separate nested scopes.
	for (
		const source of [
			'{"id":1,"id":2}',
			'{"id":1,"\\u0069d":2}',
			'{"claim":{"id":1,"id":2}}',
			'[{"id":1,"id":2}]',
		]
	) {
		assert.throws(() => parseJSON(source), /Duplicate/);
	}
	assert.deepEqual(
		parseJSON('{"left":{"id":1},"right":{"id":2},"text":"{,\\"id\\":3}"}'),
		{
			left: { id: 1 },
			right: { id: 2 },
			text: '{,"id":3}',
		},
	);
});
