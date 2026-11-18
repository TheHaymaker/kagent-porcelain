// Structured JSON logging for Cloud Logging: one JSON object per stdout line.
// Cloud Logging reads `severity` and `message`, and groups lines of one request
// by `logging.googleapis.com/trace`.

const SEVERITIES = ['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'];
const RESERVED = new Set(['severity', 'message', 'time']);

export function traceFromHeaders(headers, projectId) {
  if (!projectId) return undefined;
  let traceId;
  const cloud = headers['x-cloud-trace-context'];
  if (cloud) {
    traceId = cloud.split('/')[0];
  } else if (headers.traceparent) {
    traceId = headers.traceparent.split('-')[1];
  }
  return traceId ? `projects/${projectId}/traces/${traceId}` : undefined;
}

export function createLogger({ write = (line) => process.stdout.write(line + '\n'), level = process.env.LOG_LEVEL ?? 'INFO' } = {}) {
  const threshold = Math.max(0, SEVERITIES.indexOf(level.toUpperCase()));
  const log = (severity, message, fields = {}) => {
    if (SEVERITIES.indexOf(severity) < threshold) return;
    const entry = { severity, message, time: new Date().toISOString() };
    for (const [key, value] of Object.entries(fields)) {
      if (!RESERVED.has(key) && value !== undefined) entry[key] = value;
    }
    write(JSON.stringify(entry));
  };
  return {
    debug: (m, f) => log('DEBUG', m, f),
    info: (m, f) => log('INFO', m, f),
    warn: (m, f) => log('WARNING', m, f),
    error: (m, f) => log('ERROR', m, f),
  };
}
