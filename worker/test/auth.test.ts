import { describe, expect, test } from 'bun:test';
import type { JWTPayload, JWTVerifyGetKey } from 'jose';
import { generateKeyPair, SignJWT } from 'jose';
import { consumer, githubIssuer, gitlabIssuer, publisher, verify } from '#auth';
import { submission } from '#model';

const origin = 'https://registry.actionlint.kjanat.dev';
const s = submission({
	schema_version: 1,
	provider: 'github',
	repository: 'acme/plugin',
	tag: 'v1',
	asset: 'plugin.wasm',
	sha256: 'a'.repeat(64),
	kind: 'core',
});
const gh: JWTPayload = {
	iss: githubIssuer,
	sub: 'repo:acme/plugin:ref:refs/tags/v1',
	repository: 'acme/plugin',
	repository_id: '10',
	repository_owner_id: '20',
	repository_visibility: 'public',
	ref: 'refs/tags/v1',
	ref_type: 'tag',
	event_name: 'push',
	sha: 'a'.repeat(40),
	workflow_ref: 'acme/plugin/.github/workflows/release.yml@refs/tags/v1',
	run_id: '30',
	run_attempt: '1',
	jti: 'one',
	exp: 2000000000,
	iat: 1999999700,
};

describe('publisher claims', () => {
	test('accepts own public tag and rejects cross repository, PR and branch claims', () => {
		expect(publisher(gh, s).identity.repository_id).toBe('10');
		for (
			const sub of [
				'repo:acme@20/plugin@10:ref:refs/tags/v1',
				'custom:publisher:subject',
			]
		) {
			expect(publisher({ ...gh, sub }, s).identity.subject).toBe(sub);
		}
		for (
			const patch of [
				{ repository: 'attacker/plugin' },
				{ event_name: 'pull_request' },
				{ event_name: 'pull_request_target' },
				{ repository_visibility: 'private' },
				{ ref_type: 'branch' },
				{ workflow_ref: 'acme/other/.github/workflows/release.yml@refs/tags/v1' },
			]
		) {
			expect(() => publisher({ ...gh, ...patch }, s)).toThrow();
		}
	});
	test('GitLab uses job project identity and denies MR or foreign configuration', () => {
		const gl: JWTPayload = {
			iss: gitlabIssuer,
			sub: 'project_id:10:ref_type:tag:ref:v1',
			job_project_id: '10',
			job_namespace_id: '20',
			job_project_path: 'acme/group/plugin',
			project_id: '10',
			project_path: 'acme/group/plugin',
			project_visibility: 'public',
			ref: 'v1',
			ref_path: 'refs/tags/v1',
			ref_type: 'tag',
			pipeline_source: 'push',
			sha: 'a'.repeat(40),
			ci_config_sha: 'a'.repeat(40),
			ci_config_ref_uri: 'gitlab.com/acme/group/plugin//.gitlab-ci.yml@refs/tags/v1',
			pipeline_id: '30',
			job_id: '40',
			exp: 2000000000,
			iat: 1999999700,
			jti: 'one',
		};
		const input = submission({
			...s,
			provider: 'gitlab',
			repository: 'acme/group/plugin',
		});
		expect(publisher(gl, input).identity.repository_id).toBe('10');
		expect(
			publisher({ ...gl, sub: 'custom:gitlab:subject' }, input).identity
				.subject,
		).toBe('custom:gitlab:subject');
		for (
			const patch of [
				{ pipeline_source: 'merge_request_event' },
				{ job_project_id: '99' },
				{
					ci_config_ref_uri: null,
				},
				{ ci_config_sha: 'b'.repeat(40) },
			]
		) {
			expect(() => publisher({ ...gl, ...patch }, input)).toThrow();
		}
	});
	test('consumer exact repository ID, workflow, branch and safe event', () => {
		const policy = {
			REGISTRY_ORIGIN: origin,
			REGISTRY_REPOSITORY_ID: '10',
			REGISTRY_WORKFLOW_REF: 'acme/plugin/.github/workflows/admission.yml@refs/heads/master',
		};
		const claims = {
			...gh,
			sub: 'repo:acme/plugin:ref:refs/heads/master',
			ref: 'refs/heads/master',
			ref_type: 'branch',
			event_name: 'schedule',
			workflow_ref: policy.REGISTRY_WORKFLOW_REF,
		};
		expect(consumer(claims, policy).run_id).toBe('30:1');
		expect(
			consumer(
				{ ...claims, sub: 'repo:acme@20/plugin@10:ref:refs/heads/master' },
				policy,
			).run_id,
		).toBe('30:1');
		expect(
			consumer({ ...claims, sub: 'custom:consumer:subject' }, policy).run_id,
		).toBe('30:1');
		for (
			const patch of [
				{ repository_id: '11' },
				{ event_name: 'push' },
				{ ref: 'refs/heads/main' },
				{
					workflow_ref: 'attacker/workflow',
				},
			]
		) {
			expect(() => consumer({ ...claims, ...patch }, policy)).toThrow();
		}
	});
});

test('JWT verifies signature, issuer, audience, expiry and max token age', async () => {
	const pair = await generateKeyPair('RS256');
	const wrong = await generateKeyPair('RS256');
	const resolve: JWTVerifyGetKey = async () => pair.publicKey;
	const keys = new Map([[githubIssuer, resolve]]);
	const now = Math.floor(Date.now() / 1000);
	async function token(
		patch: JWTPayload = {},
		badKey = false,
	): Promise<string> {
		return new SignJWT({
			...gh,
			aud: origin,
			iat: now,
			nbf: now - 1,
			exp: now + 300,
			...patch,
		})
			.setProtectedHeader({ alg: 'RS256' })
			.sign(badKey ? wrong.privateKey : pair.privateKey);
	}
	expect((await verify(await token(), origin, keys)).iss).toBe(githubIssuer);
	for (
		const signed of [
			await token({}, true),
			await token({ iss: 'https://evil.example' }),
			await token({ aud: `${origin}/validator` }),
			await token({ exp: now - 60 }),
			await token({ iat: now - 700 }),
			await token({ nbf: now + 60 }),
		]
	) {
		await expect(verify(signed, origin, keys)).rejects.toThrow(
			'The OIDC token is invalid or has expired.',
		);
	}
});

test('strict submission rejects extra fields and host-like repositories', () => {
	expect(() => submission({ ...s, url: 'https://evil.example' })).toThrow();
	expect(() => submission({ ...s, repository: 'https://github.com/acme/plugin' })).toThrow();
	expect(() => submission({ ...s, provider: 'gitea' })).toThrow();
});
