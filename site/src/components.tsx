/** @jsxImportSource preact */
import { Fragment } from 'preact';
import type { ComponentChildren } from 'preact';
import { pageSize, selectPlugins, stateLink } from '@/src/directory.ts';
import type { DirectoryState } from '@/src/directory.ts';
import type { Metric, Plugin, PluginSummary, SiteData } from '@/src/schema.ts';
import { localTime } from '@/src/time.ts';

const brand =
	'https://cdn.jsdelivr.net/gh/kjanat/actionlint@51c4b21a2f9ef503fa857c9a0a5099cad981fb76/docs/assets/brand/';
const publishing = 'https://github.com/kjanat/actionlint-registry/blob/master/docs/publishing.md';

export function Time({ value, compact = false }: { value: string; compact?: boolean }) {
	const browser = typeof window !== 'undefined';
	return (
		<time dateTime={value} data-date-only={compact ? 'true' : undefined}>
			{localTime(value, browser ? undefined : 'en-GB', browser ? undefined : 'UTC', compact)}
			{compact && !browser ? ' UTC' : ''}
		</time>
	);
}

export function Count({ metric }: { metric: Metric }) {
	return metric.state === 'available'
		? (
			<span data-count={metric.count}>
				{typeof window === 'undefined' ? metric.count : new Intl.NumberFormat().format(metric.count)}
			</span>
		)
		: <span>{metric.state}</span>;
}

function Header({ root }: { root: string }) {
	return (
		<>
			<a class='skip' href='#main'>Skip to content</a>
			<header class='masthead'>
				<div class='wrap masthead-inner'>
					<a class='brand' href={`${root}index.html`} aria-label='actionlint plugin registry home'>
						<picture class='brand-logo'>
							<source media='(prefers-color-scheme: dark)' srcSet={`${brand}actionlint-dark.svg`} />
							<img src={`${brand}actionlint.svg`} alt='actionlint' width='260' height='66' />
						</picture>
						<img class='brand-mark' src={`${brand}actionlint-mark.svg`} alt='actionlint' width='40' height='40' />
						<span>Plugin registry</span>
					</a>
					<nav aria-label='Registry'>
						<a href={`${root}index.json`}>JSON index</a>
						<a href={publishing}>Publish a plugin</a>
					</nav>
				</div>
			</header>
		</>
	);
}

function Help({ root, observedAt }: { root: string; observedAt: string | null }) {
	return (
		<details class='help'>
			<summary>About this catalogue and download counts</summary>
			<p>
				Publisher identities and checksums are verified during admission. Acceptance does not guarantee safety or
				correctness. <a href={`${root}statuses.json`}>Release statuses</a>.
			</p>
			<p>
				Downloads count accepted GitHub assets, including CI and repeat downloads, not unique users or installations.
				Totals require complete counts for every accepted version; unknown counts are not zero.
			</p>
			{observedAt && (
				<p>
					Observed <Time value={observedAt} />. Refreshes every six hours.{' '}
					<a href={`${root}downloads.json`}>Download counts JSON</a>.
				</p>
			)}
		</details>
	);
}

export function Layout(
	{ root, title, children, observedAt }: {
		root: string;
		title: string;
		children: ComponentChildren;
		observedAt: string | null;
	},
) {
	return (
		<html lang='en'>
			<head>
				<meta charSet='utf-8' />
				<meta name='viewport' content='width=device-width, initial-scale=1' />
				<meta name='color-scheme' content='light dark' />
				<title>{title}</title>
				<link rel='stylesheet' href={`${root}assets/site.css`} />
				<link rel='icon' href={`${brand}actionlint-mark.svg`} type='image/svg+xml' />
				<script type='module' src={`${root}assets/client.js`} />
			</head>
			<body>
				<Header root={root} />
				<main id='main' class='wrap' tabIndex={-1}>
					{children}
					<Help root={root} observedAt={observedAt} />
				</main>
				<footer>
					<div class='wrap'>
						<a href='https://github.com/kjanat/actionlint'>actionlint</a> /{' '}
						<a href='https://github.com/kjanat/actionlint-registry'>Registry source</a>
					</div>
				</footer>
			</body>
		</html>
	);
}

export type NavigationSource = 'controls' | 'pagination';

interface DirectoryProps {
	root: string;
	plugins: PluginSummary[];
	state: DirectoryState;
	onNavigate?: (state: DirectoryState, source: NavigationSource) => void;
}

function plainClick(event: MouseEvent): boolean {
	return event.button === 0 && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey;
}

export function Directory({ root, plugins, state, onNavigate }: DirectoryProps) {
	const selected = selectPlugins(plugins, state);
	const pages = [...new Set([1, selected.page - 1, selected.page, selected.page + 1, selected.pages])].filter(page =>
		page >= 1 && page <= selected.pages
	).sort((a, b) => a - b);
	const link = (page: number) => stateLink(root, { ...state, page });
	return (
		<>
			<form
				class='controls'
				action={`${root}index.html`}
				method='get'
				onSubmit={event => {
					if (!onNavigate) return;
					event.preventDefault();
					const data = new FormData(event.currentTarget);
					onNavigate({ ...state, query: String(data.get('q') ?? '').trim(), page: 1 }, 'controls');
				}}
			>
				<label class='search'>
					Search<input
						name='q'
						type='search'
						disabled={!onNavigate}
						defaultValue={state.query}
						key={state.query}
						placeholder='Name, description or repository'
					/>
				</label>
				{onNavigate && (
					<label>
						Sort<select
							name='sort'
							value={state.sort}
							onChange={event => {
								const sort = event.currentTarget.value;
								if (sort === 'name' || sort === 'downloads' || sort === 'updated') {
									onNavigate?.({ ...state, sort, page: 1 }, 'controls');
								}
							}}
						>
							<option value='downloads'>Downloads</option>
							<option value='name'>Name</option>
							<option value='updated'>Recently updated</option>
						</select>
					</label>
				)}
				<button type='submit' disabled={!onNavigate}>Search</button>
			</form>
			{!onNavigate && (
				<>
					<p class='enhancement-status' data-client-status role='status'>
						Search needs JavaScript. Sort and browse below.
					</p>
					<nav class='sort-fallback' aria-label='Sort plugins'>
						<span>Sort</span>
						<a
							href={stateLink(root, { query: '', sort: 'downloads', page: 1 })}
							aria-current={state.sort === 'downloads' ? 'page' : undefined}
						>
							Downloads
						</a>
						<a
							href={stateLink(root, { query: '', sort: 'name', page: 1 })}
							aria-current={state.sort === 'name' ? 'page' : undefined}
						>
							Name
						</a>
						<a
							href={stateLink(root, { query: '', sort: 'updated', page: 1 })}
							aria-current={state.sort === 'updated' ? 'page' : undefined}
						>
							Recently updated
						</a>
					</nav>
				</>
			)}
			<p class='result-count' role='status' tabIndex={-1}>
				{selected.count
					? `${(selected.page - 1) * pageSize + 1}–${
						Math.min(selected.page * pageSize, selected.count)
					} of ${selected.count} plugins`
					: 'No matching plugins'}
			</p>
			<ul class='plugin-list' role='list'>
				{selected.items.map(plugin => (
					<li key={plugin.path}>
						<article class='plugin-card'>
							<h2>
								<a href={`${root}${plugin.path}`}>{plugin.name}</a>
							</h2>
							<p class='repository'>{plugin.repository}</p>
							{plugin.description && <p class='description'>{plugin.description}</p>}
							<dl class='metadata compact'>
								<div>
									<dt>Downloads</dt>
									<dd>
										<Count metric={plugin.downloads} />
									</dd>
								</div>
								<div>
									<dt>Updated</dt>
									<dd>
										<code>{plugin.version}</code> <Time compact value={plugin.updatedAt} />
									</dd>
								</div>
							</dl>
						</article>
					</li>
				))}
			</ul>
			{plugins.length === 0 && (
				<p>
					No plugins published yet. <a href={publishing}>Publish your first plugin</a>.
				</p>
			)}
			{selected.pages > 1 && (
				<nav class='pagination' aria-label='Plugin pages'>
					{selected.page > 1 && (
						<a
							rel='prev'
							href={link(selected.page - 1)}
							onClick={event => {
								if (onNavigate && plainClick(event)) {
									event.preventDefault();
									onNavigate({ ...state, page: selected.page - 1 }, 'pagination');
								}
							}}
						>
							Previous
						</a>
					)}
					{pages.map((page, index) => (
						<Fragment key={page}>
							{index > 0 && page > (pages[index - 1] ?? 0) + 1 && <span aria-hidden='true'>…</span>}
							<a
								href={link(page)}
								aria-label={`Page ${page}`}
								aria-current={page === selected.page ? 'page' : undefined}
								onClick={event => {
									if (onNavigate && plainClick(event)) {
										event.preventDefault();
										onNavigate({ ...state, page }, 'pagination');
									}
								}}
							>
								{page}
							</a>
						</Fragment>
					))}
					{selected.page < selected.pages && (
						<a
							rel='next'
							href={link(selected.page + 1)}
							onClick={event => {
								if (onNavigate && plainClick(event)) {
									event.preventDefault();
									onNavigate({ ...state, page: selected.page + 1 }, 'pagination');
								}
							}}
						>
							Next
						</a>
					)}
				</nav>
			)}
		</>
	);
}

export function Home({ root, data, state }: { root: string; data: SiteData; state: DirectoryState }) {
	return (
		<Layout root={root} title='Plugins for actionlint' observedAt={data.observedAt}>
			<h1 class='intro'>Plugins for actionlint</h1>
			<div
				id='directory'
				data-root={root}
				data-sort={state.sort}
				data-page={state.page}
				data-index={`${root}assets/search-index.json`}
			>
				<Directory root={root} plugins={data.plugins} state={state} />
			</div>
		</Layout>
	);
}

export function PluginPage({ plugin, observedAt }: { plugin: Plugin; observedAt: string | null }) {
	const root = '../../';
	return (
		<Layout root={root} title={`${plugin.name} | actionlint plugins`} observedAt={observedAt}>
			<p class='breadcrumb'>
				<a href={`${root}index.html`}>All plugins</a> / {plugin.name}
			</p>
			<header class='plugin-intro'>
				<h1>{plugin.name}</h1>
				<p class='description'>{plugin.description}</p>
				<a class='repository' href={plugin.repositoryURL}>{plugin.repository}</a>
				<dl class='metadata'>
					<div>
						<dt>Plugin ID</dt>
						<dd>
							<code>{plugin.id}</code>
						</dd>
					</div>
					{plugin.license && (
						<div>
							<dt>License</dt>
							<dd>{plugin.license}</dd>
						</div>
					)}
					<div>
						<dt>Downloads</dt>
						<dd>
							<Count metric={plugin.downloads} />
						</dd>
					</div>
					<div>
						<dt>Updated</dt>
						<dd>
							<Time value={plugin.updatedAt} />
						</dd>
					</div>
				</dl>
			</header>
			<section aria-labelledby='versions'>
				<h2 id='versions'>Versions</h2>
				<details class='status-guide'>
					<summary>Release statuses</summary>
					<dl>
						<div>
							<dt>Active</dt>
							<dd>Available; not deprecated, withdrawn or revoked.</dd>
						</div>
						<div>
							<dt>Deprecated</dt>
							<dd>Available with a warning. Check the reason and replacement.</dd>
						</div>
						<div>
							<dt>Withdrawn</dt>
							<dd>Unavailable for new installations. Existing matching locks remain usable.</dd>
						</div>
						<div>
							<dt>Revoked</dt>
							<dd>Blocked for new and existing installations, including other versions of the same artifact.</dd>
						</div>
					</dl>
				</details>
				<ol class='releases' role='list'>
					{plugin.releases.map(release => (
						<li class='release' key={release.version}>
							<article>
								<div class='release-heading'>
									<h3>{release.version}</h3>
									<span class={`state ${release.state}`}>{release.state}</span>
								</div>
								<dl class='metadata compact'>
									<div>
										<dt>Format</dt>
										<dd>{release.kind}</dd>
									</div>
									<div>
										<dt>Downloads</dt>
										<dd>
											<Count metric={release.downloads} />
										</dd>
									</div>
									<div>
										<dt>Published</dt>
										<dd>
											<Time value={release.publishedAt} />
										</dd>
									</div>
								</dl>
								{(release.state === 'active' || release.state === 'deprecated') && (
									<p class='install'>
										<code>actionlint plugin install {release.reference}</code>
									</p>
								)}
								{release.reason && <p class='reason'>{release.reason}</p>}
								{release.replacement && <p class='reason'>Replacement: {release.replacement}</p>}
								<div class='actions'>
									<a href={release.pinnedURL}>Download {release.asset}</a>
									<a href={`${root}${release.record}`}>
										Publication record<span class='sr-only'>for {release.version}</span>
									</a>
								</div>
								<details>
									<summary>
										URL and checksum<span class='sr-only'>for {release.version}</span>
									</summary>
									<dl class='integrity'>
										<div>
											<dt>Pinned URL</dt>
											<dd>
												<code>{release.pinnedURL}</code>
											</dd>
										</div>
										<div>
											<dt>SHA-256</dt>
											<dd>
												<code>{release.sha256}</code>
											</dd>
										</div>
									</dl>
								</details>
							</article>
						</li>
					))}
				</ol>
			</section>
		</Layout>
	);
}
