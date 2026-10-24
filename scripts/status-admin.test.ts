import assert from 'node:assert/strict';
import { test } from 'node:test';
import { actorIdentity } from '@/scripts/status-admin.ts';

const env = {
	GITHUB_ACTOR: 'maintainer',
	GITHUB_ACTOR_ID: '123',
	GITHUB_REPOSITORY: 'owner/registry',
	GITHUB_WORKFLOW_REF: 'owner/registry/.github/workflows/status.yml@refs/heads/master',
	GITHUB_EVENT_NAME: 'workflow_dispatch',
	GITHUB_REF: 'refs/heads/master',
	GITHUB_TRIGGERING_ACTOR: 'maintainer',
	GITHUB_RUN_ID: '10',
	GITHUB_RUN_ATTEMPT: '1',
};
const admin = {
	permission: 'admin',
	role_name: 'admin',
	user: { id: 123, login: 'maintainer' },
};

test('status audit accepts admins and maintainers, rejects ordinary write permission', () => {
	// Changing a public release status requires repository administration authority. Exercise the
	// permission distinction and bind the recorded actor to GitHub's numeric identity and run.
	for (
		const permission of [
			admin,
			{ ...admin, permission: 'write', role_name: 'maintain' },
		]
	) {
		assert.deepEqual(actorIdentity(env, permission), {
			kind: 'registry-admin',
			id: '123',
			name: 'maintainer',
			workflow: env.GITHUB_WORKFLOW_REF,
			run_id: '10:1',
		});
	}
	assert.throws(
		() => actorIdentity(env, { ...admin, permission: 'write', role_name: 'write' }),
		/admin or maintain/,
	);
});

test('status changes reject reruns by another actor, untrusted branches and mismatched identity', () => {
	// A permitted user from another workflow context must not inherit a dispatcher authorization.
	// Each mutation breaks a distinct binding used to attribute a public withdrawal or revocation.
	for (
		const changed of [
			{ GITHUB_REF: 'refs/heads/feature' },
			{ GITHUB_EVENT_NAME: 'pull_request' },
			{ GITHUB_TRIGGERING_ACTOR: 'other' },
			{ GITHUB_ACTOR_ID: '999' },
			{
				GITHUB_WORKFLOW_REF: 'evil/registry/.github/workflows/status.yml@refs/heads/master',
			},
			{ GITHUB_RUN_ATTEMPT: '0' },
		]
	) {
		assert.throws(() => actorIdentity({ ...env, ...changed }, admin));
	}
});
