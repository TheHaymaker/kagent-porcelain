"""Structured JSON logging for Cloud Logging (one JSON object per stdout line).

``severity`` and ``message`` are recognised by Cloud Logging; anything passed as
``extra={"fields": {...}}`` becomes part of the entry's jsonPayload.
"""

from __future__ import annotations

import json
import logging
import os
import sys
from datetime import UTC, datetime

_SEVERITY = {
    logging.DEBUG: "DEBUG",
    logging.INFO: "INFO",
    logging.WARNING: "WARNING",
    logging.ERROR: "ERROR",
    logging.CRITICAL: "CRITICAL",
}
_RESERVED = {"severity", "message", "time", "logger"}


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        entry: dict = {
            "severity": _SEVERITY.get(record.levelno, "DEFAULT"),
            "message": record.getMessage(),
            "time": datetime.fromtimestamp(record.created, UTC).isoformat(),
            "logger": record.name,
        }
        fields = getattr(record, "fields", None)
        if isinstance(fields, dict):
            entry.update({k: v for k, v in fields.items() if k not in _RESERVED})
        if record.exc_info:
            entry["stack_trace"] = self.formatException(record.exc_info)
        return json.dumps(entry, default=str)


_configured = False


def configure_logging() -> None:
    """Idempotent: Streamlit re-runs the script on every interaction."""
    global _configured
    if _configured:
        return
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(os.environ.get("LOG_LEVEL", "INFO").upper())
    _configured = True
