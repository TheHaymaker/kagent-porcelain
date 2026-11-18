"""Structured JSON logging for Cloud Logging.

Cloud Run ships every stdout line to Cloud Logging. A line that is a JSON object
becomes a structured entry: ``severity`` sets the level, ``message`` the summary,
and ``logging.googleapis.com/trace`` links the entry to the request's trace so
all lines of one request group together in the Logs Explorer.
"""

from __future__ import annotations

import contextvars
import json
import logging
import os
import sys
from datetime import UTC, datetime

trace_context: contextvars.ContextVar[str | None] = contextvars.ContextVar(
    "trace_context", default=None
)

_SEVERITY = {
    logging.DEBUG: "DEBUG",
    logging.INFO: "INFO",
    logging.WARNING: "WARNING",
    logging.ERROR: "ERROR",
    logging.CRITICAL: "CRITICAL",
}

# Fields a caller may attach with logger.info("...", extra={"fields": {...}}).
_RESERVED = {"severity", "message", "time", "logger"}


def trace_from_headers(headers, project_id: str | None) -> str | None:
    """Return the Cloud Logging trace resource name for a request, if any."""
    if not project_id:
        return None
    header = headers.get("x-cloud-trace-context")
    trace_id = None
    if header:
        trace_id = header.split("/", 1)[0]
    else:
        traceparent = headers.get("traceparent")
        if traceparent:
            parts = traceparent.split("-")
            if len(parts) >= 2:
                trace_id = parts[1]
    if not trace_id:
        return None
    return f"projects/{project_id}/traces/{trace_id}"


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        entry: dict = {
            "severity": _SEVERITY.get(record.levelno, "DEFAULT"),
            "message": record.getMessage(),
            "time": datetime.fromtimestamp(record.created, UTC).isoformat(),
            "logger": record.name,
        }
        trace = trace_context.get()
        if trace:
            entry["logging.googleapis.com/trace"] = trace
        fields = getattr(record, "fields", None)
        if isinstance(fields, dict):
            for key, value in fields.items():
                if key not in _RESERVED:
                    entry[key] = value
        if record.exc_info:
            entry["stack_trace"] = self.formatException(record.exc_info)
        return json.dumps(entry, default=str)


def configure_logging() -> None:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(os.environ.get("LOG_LEVEL", "INFO").upper())
    # We log one structured line per request ourselves.
    logging.getLogger("uvicorn.access").disabled = True
