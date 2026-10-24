import { mkdir, open, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { oidcToken, output, parseJSON, requestJSON, uuidValue } from '@/scripts/http.ts';

const origin = 'https://registry.actionlint.kjanat.dev';
const leaseLimit = 1024;
// A compact admission/merge report holds one stored record (2 MiB), plus envelope.
const reportLimit = 2 * 1024 * 1024 + 32 * 1024;

interface Client {
	token(): Promise<string>;
	request(url: string, token: string, payload: unknown): Promise<unknown>;
	output(name: string, value: string): void | Promise<void>;
}

function object(value: unknown): Record<string, unknown> {
	if (value === null || typeof value !== 'object' || Array.isArray(value)) {
		throw new Error('JSON object required.');
	}
	return Object.fromEntries(Object.entries(value));
}

function exactKeys(value: Record<string, unknown>, expected: string[]): void {
	if (Object.keys(value).sort().join(',') !== [...expected].sort().join(',')) {
		throw new Error('Unexpected response fields.');
	}
}

async function readObject(path: string, limit: number): Promise<Record<string, unknown>> {
	const file = await open(path, 'r');
	try {
		const buffer = Buffer.alloc(limit + 1);
		let size = 0;
		while (size < buffer.length) {
			const { bytesRead } = await file.read(buffer, size, buffer.length - size);
			if (bytesRead === 0) break;
			size += bytesRead;
		}
		if (size > limit) throw new Error(`JSON file exceeds ${limit} bytes.`);
		return object(parseJSON(buffer.subarray(0, size).toString('utf8')));
	} finally {
		await file.close();
	}
}

async function readLease(
	path: string,
): Promise<{ id: string; lease_id: string }> {
	const value = await readObject(path, leaseLimit);
	exactKeys(value, ['id', 'lease_id']);
	return { id: uuidValue(value.id), lease_id: uuidValue(value.lease_id) };
}

export async function claim(directory: string, client: Client): Promise<void> {
	const result = object(
		await client.request(`${origin}/v1/claims`, await client.token(), {}),
	);
	exactKeys(result, ['claim']);
	if (result.claim === null) {
		await client.output('claimed', 'false');
		return;
	}
	const item = object(result.claim);
	exactKeys(item, ['receipt', 'lease_id']);
	const receipt = object(item.receipt);
	if (receipt.schema_version !== 1) {
		throw new Error('Invalid authenticated receipt.');
	}
	object(receipt.submission);
	object(receipt.identity);
	const lease = {
		id: uuidValue(receipt.id),
		lease_id: uuidValue(item.lease_id),
	};
	await mkdir(directory, { recursive: true });
	await writeFile(
		join(directory, 'receipt.json'),
		`${JSON.stringify(receipt)}\n`,
	);
	await writeFile(join(directory, 'lease.json'), `${JSON.stringify(lease)}\n`);
	await client.output('claimed', 'true');
}

export async function complete(
	path: string,
	status: 'published' | 'rejected',
	client: Client,
): Promise<void> {
	const lease = await readLease(path);
	const result = object(
		await client.request(
			`${origin}/v1/claims/${lease.id}/complete`,
			await client.token(),
			{
				lease_id: lease.lease_id,
				status,
			},
		),
	);
	if (result.id !== lease.id || result.status !== status) {
		throw new Error('Unexpected completion response.');
	}
}

export async function recover(
	path: string,
	reports: string[],
	client: Client,
): Promise<void> {
	const lease = await readLease(path);
	for (const reportPath of reports) {
		let report: Record<string, unknown>;
		try {
			report = await readObject(reportPath, reportLimit);
		} catch (error) {
			if (error instanceof Error && 'code' in error && error.code === 'ENOENT') {
				continue;
			}
			throw error;
		}
		if (Array.isArray(report.rejected) && report.rejected.length === 0) {
			continue;
		}
		if (
			report.schema_version !== 1
			|| !['admit', 'merge'].includes(String(report.mode))
			|| report.submission_id !== lease.id
			|| !Array.isArray(report.accepted)
			|| report.accepted.length !== 0
			|| !Array.isArray(report.rejected)
			|| report.rejected.length !== 1
		) {
			throw new Error('Invalid submission failure report.');
		}
		const failure = object(report.rejected[0]);
		if (failure.disposition === 'rejected') {
			await complete(path, 'rejected', client);
			return;
		}
		if (failure.disposition !== 'retryable') {
			throw new Error('Unknown failure disposition.');
		}
	}
	const result = object(
		await client.request(
			`${origin}/v1/claims/${lease.id}/retry`,
			await client.token(),
			{
				lease_id: lease.lease_id,
			},
		),
	);
	if (
		result.id !== lease.id
		|| !['pending', 'failed', 'published', 'rejected'].includes(
			String(result.status),
		)
	) {
		throw new Error('Unexpected retry response.');
	}
	console.log(`Registry recovery: ${String(result.status)}`);
}

export function createClient(env = process.env): Client {
	return {
		token: () => oidcToken(`${origin}/validator`, env),
		request: (url, token, payload) => requestJSON(url, token, payload),
		output,
	};
}
