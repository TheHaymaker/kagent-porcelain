import json
import logging

from app.logging_config import JsonFormatter, trace_context, trace_from_headers


def test_trace_from_cloud_trace_header():
    headers = {"x-cloud-trace-context": "abc123/456;o=1"}
    assert trace_from_headers(headers, "proj") == "projects/proj/traces/abc123"


def test_trace_from_traceparent():
    headers = {"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
    assert trace_from_headers(headers, "proj") == (
        "projects/proj/traces/0af7651916cd43dd8448eb211c80319c"
    )


def test_no_trace_without_project():
    assert trace_from_headers({"x-cloud-trace-context": "abc/1"}, None) is None


def test_formatter_emits_cloud_logging_fields():
    record = logging.LogRecord("app", logging.WARNING, __file__, 1, "hello %s", ("world",), None)
    record.fields = {"item_id": 7, "severity": "ignored"}
    token = trace_context.set("projects/p/traces/t")
    try:
        entry = json.loads(JsonFormatter().format(record))
    finally:
        trace_context.reset(token)
    assert entry["severity"] == "WARNING"
    assert entry["message"] == "hello world"
    assert entry["item_id"] == 7
    assert entry["logging.googleapis.com/trace"] == "projects/p/traces/t"
