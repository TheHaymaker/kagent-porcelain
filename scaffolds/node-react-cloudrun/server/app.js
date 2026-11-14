import path from 'node:path';
import { fileURLToPath } from 'node:url';
import express from 'express';
import { identityMiddleware } from './identity.js';
import { createLogger, traceFromHeaders } from './logger.js';
import { MemoryStore, ValidationError } from './store.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const defaultClientDir = path.resolve(here, '../dist/client');

export function createApp({
  store = new MemoryStore(),
  logger = createLogger(),
  env = process.env,
  verify,
  clientDir = defaultClientDir,
} = {}) {
  const app = express();
  app.disable('x-powered-by');
  app.set('trust proxy', true);
  const projectId = env.GOOGLE_CLOUD_PROJECT;

  // Health first: no sign-in, no dependencies, not logged.
  app.get('/healthz', (_req, res) => res.json({ status: 'ok' }));

  app.use((req, res, next) => {
    const start = process.hrtime.bigint();
    const trace = traceFromHeaders(req.headers, projectId);
    req.log = {
      info: (m, f) => logger.info(m, { ...f, 'logging.googleapis.com/trace': trace }),
      error: (m, f) => logger.error(m, { ...f, 'logging.googleapis.com/trace': trace }),
    };
    res.set({
      'X-Content-Type-Options': 'nosniff',
      'X-Frame-Options': 'DENY',
      'Content-Security-Policy': "default-src 'self'; img-src 'self' data:",
      'Referrer-Policy': 'same-origin',
    });
    // Capture now: routers rewrite req.url/req.path while handling.
    const urlPath = req.originalUrl.split('?')[0];
    res.on('finish', () => {
      const seconds = Number(process.hrtime.bigint() - start) / 1e9;
      req.log.info(`${req.method} ${urlPath} ${res.statusCode}`, {
        httpRequest: {
          requestMethod: req.method,
          requestUrl: urlPath,
          status: res.statusCode,
          latency: `${seconds.toFixed(6)}s`,
          userAgent: req.get('user-agent') ?? '',
        },
      });
    });
    next();
  });

  app.use(identityMiddleware({ env, verify }));

  const api = express.Router();
  api.use(express.json({ limit: '100kb' }));

  api.get('/me', (req, res) => res.json({ email: req.user.email }));

  api.get('/items', (_req, res) => res.json({ items: store.list() }));

  api.post('/items', (req, res) => {
    try {
      const item = store.add(req.body?.title, req.user.email);
      req.log.info('item added', { itemId: item.id });
      res.status(201).json(item);
    } catch (err) {
      if (err instanceof ValidationError) return res.status(422).json({ error: err.message });
      throw err;
    }
  });

  api.post('/items/:id/toggle', (req, res) => {
    const item = store.toggle(Number(req.params.id));
    if (!item) return res.status(404).json({ error: 'No such item' });
    res.json(item);
  });

  api.use((_req, res) => res.status(404).json({ error: 'Not found' }));
  app.use('/api', api);

  // The built React app, with SPA fallback for client-side routes.
  app.use(express.static(clientDir, { index: false, maxAge: '1h' }));
  app.get('/{*splat}', (_req, res, next) =>
    res.sendFile(path.join(clientDir, 'index.html'), (err) => err && next()),
  );

  // eslint-disable-next-line no-unused-vars
  app.use((err, req, res, _next) => {
    (req.log ?? logger).error('unhandled error', { stack_trace: err.stack });
    res.status(500).json({ error: 'Something went wrong' });
  });

  return app;
}
