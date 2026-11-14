import { createApp } from './app.js';
import { createLogger } from './logger.js';

const logger = createLogger();
const port = Number(process.env.PORT ?? 8080);
const server = createApp({ logger }).listen(port, () => logger.info(`listening on ${port}`));

// Cloud Run sends SIGTERM before stopping an instance.
process.on('SIGTERM', () => {
  logger.info('shutting down');
  server.close(() => process.exit(0));
});
