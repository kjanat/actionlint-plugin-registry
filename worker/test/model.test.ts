import { expect, test } from 'bun:test';
import { HttpError, text } from '#model';

test('Text validation rejects ASCII control characters anywhere in a value', () => {
	for (let code = 0; code < 32; code++) {
		for (const value of [String.fromCharCode(code), `before${String.fromCharCode(code)}after`]) {
			expect(() => text(value)).toThrow(HttpError);
		}
	}
	expect(() => text(`before${String.fromCharCode(127)}after`)).toThrow(HttpError);
});

test('Text validation preserves printable ASCII and Unicode within the length limit', () => {
	for (const value of [' ', '~', 'https://github.com/kjanat/actionlint', 'café', '日本語', '🎨']) {
		expect(text(value, value.length)).toBe(value);
	}
	for (const value of ['', null, 42]) {
		expect(() => text(value)).toThrow(HttpError);
	}
	expect(() => text('abcd', 3)).toThrow(HttpError);
});
