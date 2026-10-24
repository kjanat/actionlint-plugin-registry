export function localTime(value: string, locale?: string, timeZone?: string, dateOnly = false): string {
	return new Intl.DateTimeFormat(locale, {
		year: 'numeric',
		month: 'short',
		day: 'numeric',
		...(dateOnly ? {} : { hour: '2-digit', minute: '2-digit', timeZoneName: 'short' }),
		...(timeZone ? { timeZone } : {}),
	}).format(new Date(value));
}

export function localize(root: ParentNode = document): void {
	for (const time of root.querySelectorAll('time[datetime]')) {
		if (time instanceof HTMLTimeElement) {
			time.textContent = localTime(time.dateTime, undefined, undefined, time.dataset.dateOnly === 'true');
		}
	}
	for (const count of root.querySelectorAll('[data-count]')) {
		if (count instanceof HTMLElement) count.textContent = new Intl.NumberFormat().format(Number(count.dataset.count));
	}
}
