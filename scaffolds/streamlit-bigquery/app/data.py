"""Read-only BigQuery access with a hard cost ceiling.

Every query:
- runs as the app's read-only runtime service account (IAM.md);
- uses query parameters, never string formatting, for anything a user picks;
- sets ``maximum_bytes_billed``, so BigQuery refuses (instead of billing) a
  query that would scan more than ``MAX_BYTES_BILLED`` bytes;
- carries labels so its cost shows up per app in billing exports.
"""

from __future__ import annotations

import logging
import os
import re
from dataclasses import dataclass, field
from typing import Any

log = logging.getLogger(__name__)

DEFAULT_MAX_BYTES_BILLED = 1 * 1024**3  # 1 GiB per query
_LABEL_RE = re.compile(r"[^a-z0-9_-]")

# Only SELECT/WITH statements are allowed through this module. The runtime
# service account cannot write anyway; this catches mistakes early and clearly.
_READ_ONLY_RE = re.compile(r"^\s*(--[^\n]*\n\s*)*(select|with)\b", re.IGNORECASE)


class QueryTooExpensive(Exception):
    """The query would scan more than the configured limit."""


@dataclass(frozen=True)
class QueryParam:
    name: str
    type: str  # BigQuery standard SQL type, e.g. STRING, INT64, DATE
    value: Any


@dataclass
class Settings:
    project: str | None = field(default_factory=lambda: os.environ.get("BQ_PROJECT"))
    location: str | None = field(default_factory=lambda: os.environ.get("BQ_LOCATION"))
    max_bytes_billed: int = field(
        default_factory=lambda: int(
            os.environ.get("MAX_BYTES_BILLED", str(DEFAULT_MAX_BYTES_BILLED))
        )
    )
    app_label: str = field(
        default_factory=lambda: _LABEL_RE.sub(
            "-", os.environ.get("APP_NAME", "workbench-app").lower()
        )[:63]
    )


def build_job_config(params: list[QueryParam], settings: Settings, *, dry_run: bool = False):
    from google.cloud import bigquery

    return bigquery.QueryJobConfig(
        query_parameters=[bigquery.ScalarQueryParameter(p.name, p.type, p.value) for p in params],
        maximum_bytes_billed=settings.max_bytes_billed,
        use_query_cache=True,
        dry_run=dry_run,
        labels={"workbench-app": settings.app_label},
    )


def ensure_read_only(sql: str) -> None:
    if not _READ_ONLY_RE.match(sql):
        raise ValueError("only SELECT queries are allowed")


class QueryRunner:
    def __init__(self, client=None, settings: Settings | None = None) -> None:
        self.settings = settings or Settings()
        self._client = client

    @property
    def client(self):
        if self._client is None:
            from google.cloud import bigquery

            self._client = bigquery.Client(
                project=self.settings.project, location=self.settings.location
            )
        return self._client

    def estimate_bytes(self, sql: str, params: list[QueryParam]) -> int:
        ensure_read_only(sql)
        job = self.client.query(
            sql, job_config=build_job_config(params, self.settings, dry_run=True)
        )
        return int(job.total_bytes_processed or 0)

    def run(self, sql: str, params: list[QueryParam]):
        """Run a read-only query and return a pandas DataFrame."""
        ensure_read_only(sql)
        estimate = self.estimate_bytes(sql, params)
        if estimate > self.settings.max_bytes_billed:
            raise QueryTooExpensive(
                f"query would scan {estimate} bytes, limit is {self.settings.max_bytes_billed}"
            )
        job = self.client.query(sql, job_config=build_job_config(params, self.settings))
        frame = job.result().to_dataframe(create_bqstorage_client=False)
        log.info(
            "query finished",
            extra={
                "fields": {
                    "bigquery": {
                        "jobId": getattr(job, "job_id", None),
                        "bytesBilled": getattr(job, "total_bytes_billed", None),
                        "rows": len(frame),
                    }
                }
            },
        )
        return frame


TOP_NAMES_SQL = """
SELECT name, SUM(number) AS total
FROM `bigquery-public-data.usa_names.usa_1910_current`
WHERE state = @state AND year BETWEEN @from_year AND @to_year
GROUP BY name
ORDER BY total DESC
LIMIT @limit
"""


def top_names(runner: QueryRunner, state: str, from_year: int, to_year: int, limit: int = 20):
    """The example query. Replace with the requester's own."""
    if from_year > to_year:
        raise ValueError("from_year must not be after to_year")
    return runner.run(
        TOP_NAMES_SQL,
        [
            QueryParam("state", "STRING", state),
            QueryParam("from_year", "INT64", from_year),
            QueryParam("to_year", "INT64", to_year),
            QueryParam("limit", "INT64", limit),
        ],
    )
