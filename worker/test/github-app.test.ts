import { beforeAll, describe, expect, test } from 'bun:test';
import { createPrivateKey } from 'node:crypto';
import { exportPKCS8, exportSPKI, generateKeyPair, importSPKI, jwtVerify } from 'jose';
import type { GitHubAppConfig, GitHubFetch } from '#github-app';
import { dispatchAdmission } from '#github-app';

let privateKey = '';
let githubPrivateKey = '';
let publicKey = '';

beforeAll(async () => {
	const pair = await generateKeyPair('RS256', { extractable: true });
	privateKey = await exportPKCS8(pair.privateKey);
	publicKey = await exportSPKI(pair.publicKey);
	githubPrivateKey = createPrivateKey(privateKey).export({ format: 'pem', type: 'pkcs1' }).toString();
});

function config(key = privateKey): GitHubAppConfig {
	return {
		GITHUB_APP_CLIENT_ID: 'Iv23testapp',
		GITHUB_APP_INSTALLATION_ID: '1234',
		GITHUB_APP_PRIVATE_KEY: key,
		REGISTRY_REPOSITORY_ID: '1424600475',
	};
}

function grant(): Record<string, unknown> {
	return {
		token: 'ghs_test_installation_token',
		expires_at: new Date(Date.now() + 3600000).toISOString(),
		permissions: { actions: 'write', metadata: 'read' },
		repositories: [{ id: 1424600475, full_name: 'kjanat/actionlint-registry' }],
	};
}

describe('GitHub App dispatch', () => {
	for (const format of ['pkcs1', 'pkcs8']) {
		test(`It signs with a ${format} private key and requests a scoped installation token.`, async () => {
			const requests: Request[] = [];
			const fetcher: GitHubFetch = async request => {
				requests.push(request);
				if (requests.length === 1) {
					expect(request.url).toBe('https://api.github.com/app/installations/1234/access_tokens');
					expect(await request.json()).toEqual({
						repository_ids: [1424600475],
						permissions: { actions: 'write' },
					});
					const authorization = request.headers.get('authorization');
					if (!authorization?.startsWith('Bearer ')) throw new Error('The App JWT is missing.');
					const verified = await jwtVerify(authorization.slice(7), await importSPKI(publicKey, 'RS256'), {
						algorithms: ['RS256'],
						issuer: 'Iv23testapp',
					});
					expect(verified.payload.exp).toBeGreaterThan(Math.floor(Date.now() / 1000));
					expect(request.redirect).toBe('error');
					expect(request.headers.get('x-github-api-version')).toBe('2026-03-10');
					return Response.json(grant(), { status: 201 });
				}
				expect(request.url).toBe(
					'https://api.github.com/repos/kjanat/actionlint-registry/actions/workflows/admission.yml/dispatches',
				);
				expect(request.headers.get('authorization')).toBe('Bearer ghs_test_installation_token');
				expect(request.redirect).toBe('error');
				expect(await request.json()).toEqual({ ref: 'master' });
				return Response.json({ workflow_run_id: 5678 });
			};
			const result = await dispatchAdmission(
				config(format === 'pkcs1' ? githubPrivateKey : privateKey),
				fetcher,
			);
			expect(result).toEqual({ runID: '5678' });
			expect(requests).toHaveLength(2);
		});
	}

	test('It accepts a legacy dispatch response without a run ID.', async () => {
		let calls = 0;
		const result = await dispatchAdmission(config(), async () =>
			++calls === 1
				? Response.json(grant(), { status: 201 })
				: new Response(null, { status: 204 }));
		expect(result).toEqual({ runID: null });
	});

	test('It distinguishes configuration, signing and GitHub HTTP failures without exposing their inputs.', async () => {
		await expect(dispatchAdmission({ ...config(), GITHUB_APP_CLIENT_ID: 'secret\nissuer' }))
			.rejects.toMatchObject({ details: { code: 'invalid', stage: 'configuration' } });
		await expect(dispatchAdmission(config('secret private key')))
			.rejects.toMatchObject({ details: { code: 'invalid', stage: 'signing' } });
		for (const failAt of [1, 2]) {
			let calls = 0;
			try {
				await dispatchAdmission(config(), async () =>
					++calls === failAt
						? Response.json({ message: 'secret token and signed URL' }, { status: 403 })
						: Response.json(grant(), { status: 201 }));
				throw new Error('Dispatch unexpectedly succeeded.');
			} catch (error) {
				expect(error).toMatchObject({
					details: {
						code: 'http-error',
						stage: failAt === 1 ? 'installation-token' : 'workflow-dispatch',
						status: 403,
					},
				});
				expect(JSON.stringify(error)).not.toContain('secret');
				expect(error).not.toHaveProperty('cause');
			}
			expect(calls).toBe(failAt);
		}
	});

	test('It classifies invalid grants separately and never dispatches with their tokens.', async () => {
		let calls = 0;
		await expect(dispatchAdmission(config(), async () => {
			calls++;
			return Response.json({ ...grant(), permissions: { actions: 'write', contents: 'write' } });
		})).rejects.toMatchObject({ details: { code: 'invalid', stage: 'installation-grant' } });
		expect(calls).toBe(1);
	});

	test('It discards network error messages and does not treat status zero as an HTTP failure.', async () => {
		await expect(dispatchAdmission(config(), async () => {
			throw new Error('secret request URL and token');
		})).rejects.toMatchObject({ details: { code: 'request-failed', stage: 'installation-token' } });
		await expect(dispatchAdmission(config(), async () => Response.error()))
			.rejects.toMatchObject({ details: { code: 'invalid', stage: 'installation-token' } });
	});

	test('It refuses invalid configuration and malformed keys before making requests.', async () => {
		for (
			const input of [
				{ ...config(), REGISTRY_REPOSITORY_ID: '99' },
				{ ...config(), GITHUB_APP_INSTALLATION_ID: '1234/other' },
				{ ...config(), GITHUB_APP_CLIENT_ID: 'bad\nissuer' },
				config('-----BEGIN RSA PRIVATE KEY-----\nYWJj\n-----END RSA PRIVATE KEY-----'),
				config(`${privateKey}\nUnexpected material`),
				config('x'.repeat(16385)),
			]
		) {
			let calls = 0;
			await expect(dispatchAdmission(input, async () => {
				calls++;
				return Response.json({});
			})).rejects.toThrow('The GitHub App could not dispatch registry validation.');
			expect(calls).toBe(0);
		}
	});

	test('It refuses installation tokens with extra permissions or a different repository.', async () => {
		for (
			const response of [
				{ ...grant(), permissions: { actions: 'read' } },
				{ ...grant(), permissions: { actions: 'write', contents: 'write' } },
				{ ...grant(), permissions: { actions: 'write', metadata: 'write' } },
				{ ...grant(), repositories: [] },
				{ ...grant(), repositories: [{ id: 99, full_name: 'kjanat/actionlint-registry' }] },
				{ ...grant(), repositories: [{ id: 1424600475, full_name: 'attacker/registry' }] },
				{ ...grant(), expires_at: new Date(0).toISOString() },
				{ ...grant(), token: 'secret\nheader' },
			]
		) {
			let calls = 0;
			await expect(dispatchAdmission(config(), async () => {
				calls++;
				return Response.json(response, { status: 201 });
			})).rejects.toThrow('The GitHub App could not dispatch registry validation.');
			expect(calls).toBe(1);
		}
	});

	test('It bounds response bodies and does not expose upstream credentials or errors.', async () => {
		for (
			const fetcher of [
				async () => Response.json({ message: 'ghs_secret' }, { status: 403 }),
				async () => new Response('x'.repeat(65537)),
				async () => new Response('not JSON'),
				async () => {
					throw new Error('Network failure for Bearer ghs_secret and PRIVATE KEY.');
				},
				async () => {
					throw new DOMException('The request timed out.', 'TimeoutError');
				},
			]
		) {
			await expect(dispatchAdmission(config(), fetcher)).rejects.toThrow(
				'The GitHub App could not dispatch registry validation.',
			);
		}
	});

	test('It rejects invalid workflow run identities and unexpected dispatch responses.', async () => {
		for (
			const response of [
				Response.json({ workflow_run_id: -1 }),
				Response.json({ workflow_run_id: '123' }),
				Response.json({ workflow_run_id: Number.MAX_SAFE_INTEGER + 1 }),
				Response.json({ workflow_run_id: 1 }, { status: 201 }),
			]
		) {
			let calls = 0;
			await expect(dispatchAdmission(config(), async () =>
				++calls === 1
					? Response.json(grant(), { status: 201 })
					: response)).rejects.toThrow('The GitHub App could not dispatch registry validation.');
		}
	});
});
