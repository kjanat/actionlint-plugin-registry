import type { PluginSummary } from '@/src/schema.ts';

export const pageSize = 20;
export type Sort = 'name' | 'downloads' | 'updated';
export interface DirectoryState {
	query: string;
	sort: Sort;
	page: number;
}

export function readState(url: URL): DirectoryState {
	const sort = url.searchParams.get('sort') ?? url.pathname.match(/\/sort\/(name|updated)\//)?.[1];
	const page = Number(url.searchParams.get('page') ?? url.pathname.match(/\/page\/(\d+)\//)?.[1] ?? 1);
	return {
		query: (url.searchParams.get('q') ?? '').trim(),
		sort: sort === 'name' || sort === 'updated' ? sort : 'downloads',
		page: Number.isSafeInteger(page) && page > 0 ? page : 1,
	};
}

export function selectPlugins(plugins: PluginSummary[], state: DirectoryState) {
	const query = state.query.toLocaleLowerCase();
	const matches = plugins.filter(plugin =>
		[plugin.name, plugin.description, plugin.repository].some(value => value.toLocaleLowerCase().includes(query))
	);
	matches.sort((a, b) => {
		let order = 0;
		if (state.sort === 'updated') order = Date.parse(b.updatedAt) - Date.parse(a.updatedAt);
		if (state.sort === 'downloads') {
			if (a.downloads.state === 'available' && b.downloads.state === 'available') {
				order = b.downloads.count - a.downloads.count;
			} else if (a.downloads.state === 'available') order = -1;
			else if (b.downloads.state === 'available') order = 1;
		}
		return order || a.name.localeCompare(b.name, 'en', { sensitivity: 'base' })
			|| a.repository.localeCompare(b.repository, 'en');
	});
	const pages = Math.max(1, Math.ceil(matches.length / pageSize));
	const page = Math.min(state.page, pages);
	return { items: matches.slice((page - 1) * pageSize, page * pageSize), count: matches.length, pages, page };
}

export function staticPath(sort: Sort, page: number): string {
	const prefix = sort === 'downloads' ? '' : `sort/${sort}/`;
	return `${prefix}${page === 1 ? '' : `page/${page}/`}index.html`;
}

export function stateLink(root: string, state: DirectoryState): string {
	const params = new URLSearchParams({ sort: state.sort, page: String(state.page) });
	if (state.query) params.set('q', state.query);
	return `${root}${staticPath(state.sort, state.page)}?${params}`;
}
