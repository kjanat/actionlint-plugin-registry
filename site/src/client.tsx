/** @jsxImportSource preact */
import { render } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import { Directory } from '@/src/components.tsx';
import { readState, selectPlugins, stateLink } from '@/src/directory.ts';
import type { DirectoryState } from '@/src/directory.ts';
import { searchIndexSchema } from '@/src/schema.ts';
import type { PluginSummary } from '@/src/schema.ts';
import { localize } from '@/src/time.ts';

function BrowserDirectory({ plugins, base }: { plugins: PluginSummary[]; base: URL }) {
	const [state, setState] = useState(readState(new URL(location.href)));
	function navigate(next: DirectoryState) {
		const selected = selectPlugins(plugins, next);
		const normalized = { ...next, page: selected.page };
		history.pushState(null, '', stateLink(base.href, normalized));
		setState(normalized);
	}
	useEffect(() => {
		const back = () => setState(readState(new URL(location.href)));
		addEventListener('popstate', back);
		return () => removeEventListener('popstate', back);
	}, []);
	return <Directory root={base.href} plugins={plugins} state={state} onNavigate={navigate} />;
}

localize();
const directory = document.getElementById('directory');
if (directory instanceof HTMLElement && directory.dataset.index && directory.dataset.root !== undefined) {
	const status = directory.querySelector('[data-client-status]');
	if (status) status.textContent = 'Loading search…';
	const base = new URL(directory.dataset.root || './', location.href);
	const endpoint = new URL(directory.dataset.index, location.href);
	void fetch(endpoint).then(async response => {
		if (!response.ok) throw new Error(`Search index HTTP ${response.status}`);
		const data: unknown = await response.json();
		const plugins = searchIndexSchema.parse(data);
		directory.replaceChildren();
		render(<BrowserDirectory plugins={plugins} base={base} />, directory);
	}).catch(() => {
		if (status) status.textContent = 'Search unavailable. Use sorting links and pagination.';
	});
}
