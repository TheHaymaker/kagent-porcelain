import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import request from 'supertest';
import { describe, expect, it } from 'vitest';
import { createApp } from '../server/app.js';
import { createLogger } from '../server/logger.js';

const ALICE = { 'x-goog-authenticated-user-email': 'accounts.google.com:alice@example.com' };

function setup({ env = {}, verify } = {}) {
  const lines = [];
  const clientDir = fs.mkdtempSync(path.join(os.tmpdir(), 'client-'));
  fs.writeFileSync(path.join(clientDir, 'index.html'), '<div id="root"></div>');
  const app = createApp({
    env: { GOOGLE_CLOUD_PROJECT: 'proj', ...env },
    logger: createLogger({ write: (l) => lines.push(JSON.parse(l)) }),
    verify,
    clientDir,
  });
  return { app, lines };
}

describe('server', () => {
  it('serves /healthz without sign-in', async () => {
    const { app } = setup();
    const res = await request(app).get('/healthz');
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ status: 'ok' });
  });

  it('requires sign-in for the API and the UI', async () => {
    const { app } = setup();
    expect((await request(app).get('/api/items')).status).toBe(401);
    expect((await request(app).get('/')).status).toBe(401);
  });

  it('returns the signed-in user', async () => {
    const { app } = setup();
    const res = await request(app).get('/api/me').set(ALICE);
    expect(res.body).toEqual({ email: 'alice@example.com' });
  });

  it('adds, lists and toggles items', async () => {
    const { app } = setup();
    const created = await request(app).post('/api/items').set(ALICE).send({ title: 'Order toner' });
    expect(created.status).toBe(201);
    expect(created.body).toMatchObject({ id: 1, title: 'Order toner', createdBy: 'alice@example.com', done: false });

    const toggled = await request(app).post('/api/items/1/toggle').set(ALICE);
    expect(toggled.body.done).toBe(true);

    const list = await request(app).get('/api/items').set(ALICE);
    expect(list.body.items).toHaveLength(1);
  });

  it('rejects blank titles with 422', async () => {
    const { app } = setup();
    const res = await request(app).post('/api/items').set(ALICE).send({ title: '  ' });
    expect(res.status).toBe(422);
    expect(res.body.error).toMatch(/must not be empty/);
  });

  it('404s unknown items and API routes', async () => {
    const { app } = setup();
    expect((await request(app).post('/api/items/9/toggle').set(ALICE)).status).toBe(404);
    expect((await request(app).get('/api/nope').set(ALICE)).status).toBe(404);
  });

  it('serves the SPA for client routes with security headers', async () => {
    const { app } = setup();
    const res = await request(app).get('/some/page').set(ALICE);
    expect(res.status).toBe(200);
    expect(res.text).toContain('id="root"');
    expect(res.headers['content-security-policy']).toContain("default-src 'self'");
    expect(res.headers['x-powered-by']).toBeUndefined();
  });

  it('logs one structured line per request with the trace', async () => {
    const { app, lines } = setup();
    await request(app).get('/api/me').set(ALICE).set('x-cloud-trace-context', 'abc/1;o=1');
    const entry = lines.find((l) => l.message === 'GET /api/me 200');
    expect(entry.severity).toBe('INFO');
    expect(entry['logging.googleapis.com/trace']).toBe('projects/proj/traces/abc');
    expect(entry.httpRequest.status).toBe(200);
  });

  it('uses the verified IAP JWT when IAP_AUDIENCE is set', async () => {
    const { app } = setup({ env: { IAP_AUDIENCE: 'aud' }, verify: async () => 'carol@example.com' });
    // The plain header alone is not accepted.
    expect((await request(app).get('/api/me').set(ALICE)).status).toBe(401);
    const res = await request(app).get('/api/me').set('x-goog-iap-jwt-assertion', 'token');
    expect(res.body.email).toBe('carol@example.com');
  });

  it('rejects an invalid IAP JWT', async () => {
    const verify = async () => {
      throw new Error('bad signature');
    };
    const { app } = setup({ env: { IAP_AUDIENCE: 'aud' }, verify });
    const res = await request(app).get('/api/me').set('x-goog-iap-jwt-assertion', 'token');
    expect(res.status).toBe(401);
  });

  it('accepts LOCAL_DEV_USER only off Cloud Run', async () => {
    const local = setup({ env: { LOCAL_DEV_USER: 'dev@example.com' } });
    expect((await request(local.app).get('/api/me')).body.email).toBe('dev@example.com');
    const cloud = setup({ env: { LOCAL_DEV_USER: 'dev@example.com', K_SERVICE: 'svc' } });
    expect((await request(cloud.app).get('/api/me')).status).toBe(401);
  });
});
