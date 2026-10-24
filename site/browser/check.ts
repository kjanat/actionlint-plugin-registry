import { expect, test } from 'bun:test';
import { mkdir, readFile } from 'node:fs/promises';
import { extname, join } from 'node:path';
import { chromium } from 'playwright';
import type { BrowserContext } from 'playwright';
import { buildSite } from '@/src/build.tsx';
import { catalogueSchema, downloadsSchema, entrySchema, statusSchema } from '@/src/schema.ts';
import { createExport, fixture } from '@/test/fixtures.ts';

test('static fallback, browser directory and locale behavior against 100 public plugins', async () => {
	const plugins = Array.from(
		{ length: 100 },
		(_, index) =>
			fixture(`Plugin ${String(index + 1).padStart(3, '0')}`, index === 99 ? null : index === 98 ? 12345 : index),
	);
	const first = plugins[0]?.versions[0];
	const second = plugins[1]?.versions[0];
	if (!first || !second) throw new Error('Missing fixtures');
	first.publishedAt = '2026-03-29T00:30:00Z';
	second.publishedAt = '2026-03-29T01:30:00Z';
	const hostile = plugins[41]?.versions[0];
	if (!hostile) throw new Error('Missing hostile fixture');
	hostile.metadata = {
		name: 'Plugin 042',
		description: '<img src=x onerror="globalThis.siteXSS=true">',
		license: 'MIT',
	};
	const input = await createExport(plugins);
	const directory = join(input, 'website');
	await buildSite(input, directory);
	const reviewDirectory = process.env.SITE_REVIEW_DIR;
	if (reviewDirectory) await mkdir(reviewDirectory, { recursive: true });
	const browser = await chromium.launch(process.env.SITE_CHROMIUM ? { executablePath: process.env.SITE_CHROMIUM } : {});
	const base = 'https://registry.test/project/';
	async function files(context: BrowserContext) {
		await context.route(`${base}**`, async route => {
			const path = new URL(route.request().url()).pathname.slice('/project/'.length) || 'index.html';
			if (path.split('/').includes('..')) throw new Error('Unexpected fixture traversal');
			const extension = extname(path);
			await route.fulfill({
				body: await readFile(join(directory, path)),
				contentType: extension === '.js'
					? 'text/javascript'
					: extension === '.css'
					? 'text/css'
					: extension === '.json'
					? 'application/json'
					: 'text/html',
			});
		});
	}
	try {
		for (const [locale, timezoneId] of [['nl-NL', 'Europe/Amsterdam'], ['en-US', 'America/Los_Angeles']]) {
			if (!locale || !timezoneId) throw new Error('Missing locale fixture');
			for (const colorScheme of ['light', 'dark']) {
				if (colorScheme !== 'light' && colorScheme !== 'dark') throw new Error('Unknown theme');
				for (const width of [320, 375, 768, 1440]) {
					const context = await browser.newContext({
						locale,
						timezoneId,
						colorScheme,
						viewport: { width, height: 900 },
					});
					await files(context);
					const page = await context.newPage();
					await page.goto(base);
					await page.waitForFunction(() => !document.querySelector('input')?.hasAttribute('disabled'));
					expect(await page.locator('.plugin-card').count()).toBe(20);
					expect(await page.locator('.pagination').count()).toBe(1);
					expect(await page.locator('.plugin-card h2').first().innerText()).toBe('Plugin 099');
					expect(await page.locator('.plugin-card [data-count]').first().innerText()).toBe(
						locale === 'nl-NL' ? '12.345' : '12,345',
					);
					expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
					expect(
						await page.locator('.brand img').evaluateAll(images =>
							images.every(image => image instanceof HTMLImageElement && image.complete && image.naturalWidth > 0)
						),
					).toBe(true);
					if (reviewDirectory && locale === 'en-US' && colorScheme === 'light' && width === 375) {
						await page.screenshot({ path: join(reviewDirectory, '100-plugins-mobile-first-page.png'), fullPage: true });
						await page.locator('.pagination').scrollIntoViewIfNeeded();
						await page.screenshot({ path: join(reviewDirectory, '100-plugins-mobile-pagination.png') });
					}
					await page.getByRole('combobox').selectOption('name');
					const date = await page.locator('.plugin-card').first().locator('time').innerText();
					expect(date).toContain(locale === 'nl-NL' ? '29 mrt' : 'Mar 28');
					await page.locator('.help summary').click();
					expect(await page.locator('.help time').innerText()).toContain(locale === 'nl-NL' ? '03:30' : '06:30');
					await context.close();
				}
			}
		}

		const context = await browser.newContext();
		await files(context);
		const page = await context.newPage();
		await page.goto(base);
		await page.getByRole('searchbox').waitFor();
		await page.waitForFunction(() =>
			document.querySelector('input') instanceof HTMLInputElement
			&& !document.querySelector('input')?.hasAttribute('disabled')
		);
		await page.getByRole('link', { name: 'Next', exact: true }).click();
		expect(await page.locator('.plugin-card').count()).toBe(20);
		expect(await page.locator('.plugin-card h2').first().innerText()).toBe('Plugin 079');
		await page.goBack();
		expect(await page.locator('.plugin-card h2').first().innerText()).toBe('Plugin 099');
		await page.getByRole('searchbox').fill('Plugin 042');
		await page.getByRole('button', { name: 'Search', exact: true }).click();
		expect(await page.locator('.plugin-card').count()).toBe(1);
		expect(await page.locator('.plugin-card h2').innerText()).toBe('Plugin 042');
		expect(await page.locator('.plugin-card .description').innerText()).toContain('<img src=x');
		expect(await page.locator('.plugin-card img').count()).toBe(0);
		expect(await page.evaluate(() => 'siteXSS' in globalThis)).toBe(false);
		expect(new URL(page.url()).searchParams.get('q')).toBe('Plugin 042');
		await page.reload();
		await page.waitForFunction(() => document.querySelectorAll('.plugin-card').length === 1);
		await page.getByRole('searchbox').fill('missing entirely');
		await page.getByRole('button', { name: 'Search', exact: true }).click();
		expect(await page.locator('.plugin-card').count()).toBe(0);
		await page.goto(base);
		await page.waitForFunction(() => !document.querySelector('input')?.hasAttribute('disabled'));
		const modified = await page.getByRole('link', { name: 'Next', exact: true }).evaluate(link => {
			const options: MouseEventInit[] = [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { button: 1 }];
			return options.map(option => {
				const event = new MouseEvent('click', { bubbles: true, cancelable: true, ...option });
				link.dispatchEvent(event);
				return event.defaultPrevented;
			});
		});
		expect(modified).toEqual([false, false, false, false]);
		await page.goto(`${base}plugins/${'1'.padStart(64, '0')}/index.html`);
		await page.locator('.release details summary').click();
		expect(await page.locator('.integrity').innerText()).toContain('#sha256=');
		expect(await page.locator('.install').innerText()).toContain('@v1.0.0');
		const [recordResponse] = await Promise.all([
			page.waitForNavigation(),
			page.getByRole('link', { name: /^Publication record/ }).click(),
		]);
		if (!recordResponse) throw new Error('No publication JSON response');
		expect(entrySchema.parse(await recordResponse.json()).submission.tag).toBe('v1.0.0');
		for (const target of ['JSON index', 'Release statuses', 'Download counts JSON']) {
			await page.goto(base);
			if (target !== 'JSON index') await page.locator('.help summary').click();
			const [response] = await Promise.all([
				page.waitForNavigation(),
				page.getByRole('link', { name: target, exact: true }).click(),
			]);
			if (!response) throw new Error(`No response for ${target}`);
			const payload: unknown = await response.json();
			if (target === 'JSON index') expect(catalogueSchema.parse(payload).plugins).toHaveLength(100);
			if (target === 'Release statuses') expect(statusSchema.parse(payload).releases).toHaveLength(0);
			if (target === 'Download counts JSON') expect(downloadsSchema.parse(payload).releases).toHaveLength(100);
		}
		await context.close();

		for (const mode of ['no-js', 'failed', 'slow']) {
			const fallback = await browser.newContext({ javaScriptEnabled: mode !== 'no-js' });
			await files(fallback);
			let release = () => {};
			const gate = new Promise<void>(resolve => {
				release = resolve;
			});
			if (mode === 'failed') await fallback.route('**/assets/search-index.json', route => route.abort());
			if (mode === 'slow') {
				await fallback.route('**/assets/search-index.json', async route => {
					await gate;
					await route.fulfill({
						body: await readFile(join(directory, 'assets/search-index.json')),
						contentType: 'application/json',
					});
				});
			}
			const view = await fallback.newPage();
			await view.goto(base);
			expect(await view.getByRole('searchbox').isDisabled()).toBe(true);
			expect(await view.getByRole('navigation', { name: 'Sort plugins' }).isVisible()).toBe(true);
			if (mode === 'failed') {
				await view.waitForFunction(() =>
					document.querySelector('[data-client-status]')?.textContent?.includes('unavailable')
				);
			}
			if (mode === 'slow') {
				release();
				await view.waitForFunction(() => !document.querySelector('input')?.hasAttribute('disabled'));
			} else {
				await view.getByRole('link', { name: 'Name', exact: true }).click();
				expect(await view.locator('.plugin-card h2').first().innerText()).toBe('Plugin 001');
				await view.getByRole('link', { name: 'Next', exact: true }).click();
				expect(await view.locator('.plugin-card h2').first().innerText()).toBe('Plugin 021');
			}
			await fallback.close();
		}
	} finally {
		await browser.close();
	}
}, 60000);
