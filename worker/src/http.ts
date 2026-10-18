import type { Policy } from '#auth';
import { bearer } from '#auth';
import { HttpError } from '#model';

export function json(value: unknown, status = 200): Response {
	return Response.json(value, {
		status,
		headers: {
			'cache-control': 'no-store',
			'x-content-type-options': 'nosniff',
		},
	});
}

/** Reject malformed routes and missing credentials before accessing the durable inbox. */
export function preflight(
	request: Request,
	policy: Policy,
): Response | undefined {
	try {
		if (
			!/^[1-9][0-9]*$/u.test(policy.REGISTRY_REPOSITORY_ID)
			|| policy.REGISTRY_ORIGIN !== 'https://registry.actionlint.kjanat.dev'
			|| policy.REGISTRY_WORKFLOW_REF
				!== 'kjanat/actionlint-plugin-registry/.github/workflows/admission.yml@refs/heads/master'
		) {
			return json({ error: 'The publication inbox is not configured correctly.' }, 503);
		}
		const url = new URL(request.url);
		const goDiscovery = (request.method === 'GET' || request.method === 'HEAD') && url.search === '?go-get=1';
		if (url.origin !== policy.REGISTRY_ORIGIN || (url.search !== '' && !goDiscovery)) {
			return json({ error: 'Use the configured API origin without query parameters.' }, 400);
		}
		if (goDiscovery) {
			return new Response(
				request.method === 'HEAD'
					? null
					: '<!doctype html><html><head><meta name="go-import" content="registry.actionlint.kjanat.dev git https://github.com/kjanat/actionlint-plugin-registry"></head></html>',
				{
					headers: {
						'content-type': 'text/html; charset=utf-8',
						'cache-control': 'public, max-age=300',
						'x-content-type-options': 'nosniff',
					},
				},
			);
		}
		if ((request.method === 'GET' || request.method === 'HEAD') && url.pathname === '/') {
			return Response.redirect('https://plugins.actionlint.kjanat.dev/', 302);
		}
		if (request.method === 'GET' && url.pathname === '/health') {
			return json({ status: 'ok' });
		}
		if (
			request.method === 'GET'
			&& /^\/v1\/submissions\/[0-9a-f-]{36}$/u.test(url.pathname)
		) {
			return undefined;
		}
		if (
			request.method !== 'POST'
			|| (!['/v1/submissions', '/v1/claims'].includes(url.pathname)
				&& !/^\/v1\/claims\/[0-9a-f-]{36}\/complete$/u.test(url.pathname))
		) {
			return json({ error: 'The requested endpoint does not exist.' }, 404);
		}
		bearer(request);
		return undefined;
	} catch (error) {
		if (error instanceof HttpError) {
			return json({ error: error.message }, error.status);
		}
		throw error;
	}
}
