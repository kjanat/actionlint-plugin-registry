import { object, text } from '#model';
import type { Database } from '#store';

export type ScheduleAlarm = (timestampMilliseconds: number) => Promise<void>;

interface Notification {
	id: string;
	attempt: number;
}

function integer(value: unknown): number {
	if (typeof value !== 'number' || !Number.isSafeInteger(value)) {
		throw new Error('The dispatch outbox contains an invalid integer.');
	}
	return value;
}

/** Notifications share the submission transaction, but never confer publication authority. */
export class DispatchOutbox {
	constructor(private readonly db: Database) {}

	next(now: number): number | null {
		return this.db.transaction(() => {
			this.db.query(
				"DELETE FROM dispatch_outbox WHERE expires<=? OR attempts>=5 OR submission_id NOT IN (SELECT id FROM submissions WHERE status='pending')",
				now,
			);
			const value = this.db.query(
				'SELECT next_attempt FROM dispatch_outbox ORDER BY next_attempt,submission_id LIMIT 1',
			)[0];
			return value === undefined ? null : integer(object(value).next_attempt);
		});
	}

	reserve(now: number): Notification | null {
		return this.db.transaction(() => {
			const found = this.db.query(
				"SELECT submission_id,attempts FROM dispatch_outbox WHERE next_attempt<=? AND expires>? AND attempts<5 AND submission_id IN (SELECT id FROM submissions WHERE status='pending') ORDER BY next_attempt,submission_id LIMIT 1",
				now,
				now,
			)[0];
			if (found === undefined) return null;
			const value = object(found);
			const id = text(value.submission_id);
			const attempt = integer(value.attempts) + 1;
			this.db.query(
				'UPDATE dispatch_outbox SET attempts=?,next_attempt=? WHERE submission_id=?',
				attempt,
				now + 30 * 2 ** (attempt - 1),
				id,
			);
			return { id, attempt };
		});
	}

	delivered(notification: Notification): void {
		this.db.query(
			'DELETE FROM dispatch_outbox WHERE submission_id=? AND attempts=?',
			notification.id,
			notification.attempt,
		);
	}
}

/** Persist the wake-up first: a crash may leave an empty alarm, never an unwakeable row. */
export async function submitWithWakeup<T>(schedule: ScheduleAlarm, now: number, submit: () => T): Promise<T> {
	await schedule(now * 1000 + 1000);
	return submit();
}

/** One dispatch per alarm bounds amplification; the scheduled workflow remains a fallback. */
export async function dispatchNotification(
	outbox: DispatchOutbox,
	schedule: ScheduleAlarm,
	dispatch: () => Promise<unknown>,
	now: number,
): Promise<void> {
	const next = outbox.next(now);
	if (next === null) return;
	if (next > now) {
		await schedule(next * 1000);
		return;
	}
	// Arm before reserving or doing external I/O, so a crash cannot strand a retry.
	await schedule((now + 30) * 1000);
	const notification = outbox.reserve(now);
	if (notification === null) return;
	try {
		await dispatch();
	} catch {
		// The retry is already durable. Do not log credentials or remote error bodies.
		return;
	}
	outbox.delivered(notification);
}
