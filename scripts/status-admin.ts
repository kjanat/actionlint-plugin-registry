import { writeFile } from 'node:fs/promises';
import { requestJSON } from '@/scripts/http.ts';

export function actorIdentity(
	env: NodeJS.ProcessEnv,
	permission: Record<string, unknown>,
) {
	const actor = env.GITHUB_ACTOR ?? '';
	const actorID = env.GITHUB_ACTOR_ID ?? '';
	const repository = env.GITHUB_REPOSITORY ?? '';
	const workflow = env.GITHUB_WORKFLOW_REF;
	if (
		env.GITHUB_EVENT_NAME !== 'workflow_dispatch'
		|| env.GITHUB_REF !== 'refs/heads/master'
		|| env.GITHUB_TRIGGERING_ACTOR !== actor
		|| workflow
			!== `${repository}/.github/workflows/status.yml@refs/heads/master`
		|| !/^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/.test(actor)
		|| !/^[1-9][0-9]*$/.test(actorID)
	) {
		throw new Error(
			'Status changes require the original dispatcher on trusted master',
		);
	}
	const user = permission.user;
	if (
		!user
		|| typeof user !== 'object'
		|| !('id' in user)
		|| String(user.id) !== actorID
		|| !('login' in user)
		|| typeof user.login !== 'string'
		|| user.login.toLowerCase() !== actor.toLowerCase()
		|| !(
			permission.permission === 'admin'
			|| (permission.role_name === 'maintain' && permission.permission === 'write')
		)
	) {
		throw new Error(
			'Status changes require registry admin or maintain permission',
		);
	}
	const runID = `${env.GITHUB_RUN_ID}:${env.GITHUB_RUN_ATTEMPT}`;
	if (!/^[1-9][0-9]*:[1-9][0-9]*$/.test(runID)) {
		throw new Error('Invalid workflow run identity');
	}
	return {
		kind: 'registry-admin',
		id: actorID,
		name: actor,
		workflow,
		run_id: runID,
	};
}

export async function authorizeStatus(
	path: string,
	env = process.env,
): Promise<void> {
	const repository = env.GITHUB_REPOSITORY ?? '';
	const actor = env.GITHUB_ACTOR ?? '';
	if (
		!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)
		|| !/^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/.test(actor)
	) {
		throw new Error('Invalid registry repository or dispatcher');
	}
	const permission = await requestJSON(
		`https://api.github.com/repos/${repository}/collaborators/${actor}/permission`,
		env.GH_TOKEN ?? '',
		undefined,
		'Read dispatcher permission',
	);
	await writeFile(path, `${JSON.stringify(actorIdentity(env, permission))}\n`);
}
