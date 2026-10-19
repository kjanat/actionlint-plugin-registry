import type { JWTPayload, JWTVerifyGetKey } from 'jose';
import { createRemoteJWKSet, decodeJwt, jwtVerify } from 'jose';
import type { AuthErrorCode, Authorization, Identity, Submission } from '#model';
import { HttpError, text } from '#model';

export const githubIssuer = 'https://token.actions.githubusercontent.com';
export const gitlabIssuer = 'https://gitlab.com';
const keys = new Map([
	[
		githubIssuer,
		createRemoteJWKSet(
			new URL('https://token.actions.githubusercontent.com/.well-known/jwks'),
			{
				timeoutDuration: 5000,
			},
		),
	],
	[
		gitlabIssuer,
		createRemoteJWKSet(new URL('https://gitlab.com/oauth/discovery/keys'), {
			timeoutDuration: 5000,
		}),
	],
]);

export interface Policy {
	REGISTRY_ORIGIN: string;
	REGISTRY_REPOSITORY_ID: string;
	REGISTRY_WORKFLOW_REF: string;
}

export async function verify(
	token: string,
	audience: string,
	testKeys?: ReadonlyMap<string, JWTVerifyGetKey>,
): Promise<JWTPayload> {
	try {
		if (token.length > 16384) throw new Error('oversize token');
		const issuer = decodeJwt(token).iss;
		const key = typeof issuer === 'string' ? (testKeys ?? keys).get(issuer) : undefined;
		if (!key || (issuer !== githubIssuer && issuer !== gitlabIssuer)) {
			throw new Error('issuer denied');
		}
		const { payload } = await jwtVerify(token, key, {
			issuer,
			audience,
			algorithms: ['RS256'],
			requiredClaims: ['iss', 'sub', 'aud', 'iat', 'nbf', 'exp', 'jti'],
			maxTokenAge: 600,
			clockTolerance: 5,
		});
		if (payload.aud !== audience) throw new Error('exact audience required');
		if (
			typeof payload.iat !== 'number'
			|| typeof payload.exp !== 'number'
			|| payload.exp <= payload.iat
		) {
			throw new Error('invalid lifetime');
		}
		return payload;
	} catch {
		throw new HttpError(401, 'The OIDC token is invalid or has expired.', 'oidc_invalid');
	}
}

function id(value: unknown): string {
	const result = text(value, 32);
	if (!/^[1-9][0-9]*$/u.test(result)) {
		throw new HttpError(403, 'The identity claim must contain a valid numeric ID.');
	}
	return result;
}

function commit(value: unknown): string {
	const result = text(value, 40);
	if (!/^[0-9a-f]{40}$/u.test(result)) {
		throw new HttpError(403, 'The commit claim must contain a full commit SHA.');
	}
	return result;
}

function githubClaim(value: unknown, code: AuthErrorCode, limit = 512, pattern?: RegExp): string {
	try {
		const result = text(value, limit);
		if (pattern === undefined || pattern.test(result)) return result;
	} catch (error) {
		if (!(error instanceof HttpError)) throw error;
	}
	throw new HttpError(403, 'A required GitHub identity claim is missing or invalid.', code);
}

function githubIdentity(p: JWTPayload): Identity {
	if (p.iss !== githubIssuer) {
		throw new HttpError(403, 'The identity must come from GitHub Actions.', 'github_issuer');
	}
	if (p.repository_visibility !== 'public') {
		throw new HttpError(403, 'The workflow must run in a public GitHub repository.', 'github_visibility');
	}
	return {
		issuer: githubIssuer,
		subject: githubClaim(p.sub, 'github_subject'),
		repository_id: githubClaim(p.repository_id, 'github_repository_id', 32, /^[1-9][0-9]*$/u),
		owner_id: githubClaim(p.repository_owner_id, 'github_owner_id', 32, /^[1-9][0-9]*$/u),
		repository: githubClaim(p.repository, 'github_repository', 256).toLowerCase(),
		ref: githubClaim(p.ref, 'github_ref'),
		commit: githubClaim(p.sha, 'github_commit', 40, /^[0-9a-f]{40}$/u),
		workflow: githubClaim(p.workflow_ref, 'github_workflow'),
		run_id: `${githubClaim(p.run_id, 'github_run_id', 32, /^[1-9][0-9]*$/u)}:${
			githubClaim(p.run_attempt, 'github_run_attempt', 32, /^[1-9][0-9]*$/u)
		}`,
	};
}

export function publisher(p: JWTPayload, s: Submission): Authorization {
	let identity: Identity;
	if (s.provider === 'github') {
		identity = githubIdentity(p);
		if (
			!['push', 'release', 'workflow_dispatch'].includes(text(p.event_name))
			|| p.ref_type !== 'tag'
		) {
			throw new HttpError(403, 'The workflow must run on a tag for a supported event.');
		}
		if (
			!identity.workflow
				.toLowerCase()
				.startsWith(`${identity.repository}/.github/workflows/`)
			|| !identity.workflow.endsWith(`@refs/tags/${s.tag}`)
		) {
			throw new HttpError(
				403,
				"The workflow must come from the submitted tag in the publisher's repository.",
			);
		}
	} else {
		if (
			p.iss !== gitlabIssuer
			|| p.project_visibility !== 'public'
			|| p.ref_type !== 'tag'
			|| !['push', 'web'].includes(text(p.pipeline_source))
		) {
			throw new HttpError(403, 'The pipeline must run on a tag in a public GitLab project.');
		}
		const repository = text(p.job_project_path, 256).toLowerCase();
		const repositoryID = id(p.job_project_id);
		if (
			id(p.project_id) !== repositoryID
			|| text(p.project_path, 256).toLowerCase() !== repository
			|| p.ref !== s.tag
			|| p.ref_path !== `refs/tags/${s.tag}`
		) {
			throw new HttpError(403, "The job must run on the submitted tag in the publisher's project.");
		}
		const subject = text(p.sub);
		identity = {
			issuer: gitlabIssuer,
			subject,
			repository_id: repositoryID,
			owner_id: id(p.job_namespace_id),
			repository,
			ref: text(p.ref_path),
			commit: commit(p.sha),
			workflow: text(p.ci_config_ref_uri),
			run_id: `${id(p.pipeline_id)}:${id(p.job_id)}`,
		};
		if (
			!identity.workflow
				.toLowerCase()
				.startsWith(`gitlab.com/${repository}//`)
			|| !identity.workflow.endsWith(`@refs/tags/${s.tag}`)
			|| commit(p.ci_config_sha) !== identity.commit
		) {
			throw new HttpError(403, 'The CI configuration must come from the submitted tag and commit.');
		}
	}
	if (
		identity.repository !== s.repository
		|| identity.ref !== `refs/tags/${s.tag}`
	) {
		throw new HttpError(403, 'The submission does not match the authenticated publisher.');
	}
	if (typeof p.exp !== 'number' || typeof p.iat !== 'number') {
		throw new HttpError(403, 'The token must include its issue and expiration times.');
	}
	return {
		identity,
		jti: text(p.jti, 256),
		expires: Math.min(p.exp, p.iat + 605),
	};
}

export function consumer(p: JWTPayload, policy: Policy): Identity {
	const identity = githubIdentity(p);
	if (identity.repository_id !== policy.REGISTRY_REPOSITORY_ID) {
		throw new HttpError(
			403,
			'The validator repository does not match the configured registry.',
			'validator_repository',
		);
	}
	if (identity.workflow !== policy.REGISTRY_WORKFLOW_REF) {
		throw new HttpError(403, 'The validator workflow does not match the configured workflow.', 'validator_workflow');
	}
	if (identity.ref !== 'refs/heads/master') {
		throw new HttpError(403, 'The validator must run on the configured master branch.', 'validator_ref');
	}
	if (p.ref_type !== 'branch') {
		throw new HttpError(403, 'The validator reference type must be a branch.', 'validator_ref_type');
	}
	if (p.event_name !== 'schedule' && p.event_name !== 'workflow_dispatch') {
		throw new HttpError(403, 'The validator event must be schedule or workflow_dispatch.', 'validator_event');
	}
	return identity;
}

export function bearer(request: Request): string {
	const header = request.headers.get('authorization');
	if (!header?.startsWith('Bearer ') || header.length > 16400) {
		throw new HttpError(401, 'Provide an OIDC token in the Authorization: Bearer header.', 'oidc_bearer_missing');
	}
	return header.slice(7);
}
