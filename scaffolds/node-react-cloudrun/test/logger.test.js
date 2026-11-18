import { describe, expect, it } from 'vitest';
import { createLogger, traceFromHeaders } from '../server/logger.js';
import { parseEmailHeader } from '../server/identity.js';

describe('logger', () => {
  it('writes Cloud Logging JSON and drops reserved field overrides', () => {
    const lines = [];
    const log = createLogger({ write: (l) => lines.push(JSON.parse(l)) });
    log.warn('careful', { itemId: 3, severity: 'DEBUG' });
    expect(lines[0]).toMatchObject({ severity: 'WARNING', message: 'careful', itemId: 3 });
  });

  it('honours the level threshold', () => {
    const lines = [];
    const log = createLogger({ write: (l) => lines.push(l), level: 'WARNING' });
    log.info('hidden');
    log.error('shown');
    expect(lines).toHaveLength(1);
  });

  it('extracts traces from both header formats', () => {
    expect(traceFromHeaders({ 'x-cloud-trace-context': 'abc/1' }, 'p')).toBe('projects/p/traces/abc');
    expect(
      traceFromHeaders({ traceparent: '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01' }, 'p'),
    ).toBe('projects/p/traces/0af7651916cd43dd8448eb211c80319c');
    expect(traceFromHeaders({ 'x-cloud-trace-context': 'abc/1' }, undefined)).toBeUndefined();
  });
});

describe('parseEmailHeader', () => {
  it.each([
    ['accounts.google.com:alice@example.com', 'alice@example.com'],
    ['bob@example.com', 'bob@example.com'],
    ['accounts.google.com:nope', undefined],
    ['', undefined],
    [undefined, undefined],
  ])('%s -> %s', (value, expected) => {
    expect(parseEmailHeader(value)).toBe(expected);
  });
});
