import pandas as pd
import pytest

from app import data


class FakeResult:
    def __init__(self, frame):
        self._frame = frame

    def to_dataframe(self, create_bqstorage_client=True):
        assert create_bqstorage_client is False
        return self._frame


class FakeJob:
    def __init__(self, bytes_processed, frame):
        self.total_bytes_processed = bytes_processed
        self.total_bytes_billed = bytes_processed
        self.job_id = "job-1"
        self._frame = frame

    def result(self):
        return FakeResult(self._frame)


class FakeClient:
    def __init__(self, bytes_processed=1000, frame=None):
        self.calls = []
        self.bytes_processed = bytes_processed
        self.frame = frame if frame is not None else pd.DataFrame({"name": ["Ada"], "total": [3]})

    def query(self, sql, job_config):
        self.calls.append((sql, job_config))
        return FakeJob(self.bytes_processed, self.frame)


def settings(**overrides):
    values = {"project": "p", "location": "US", "max_bytes_billed": 10_000, "app_label": "demo"}
    values.update(overrides)
    return data.Settings(**values)


def test_runs_dry_run_then_query_with_limits_and_params():
    client = FakeClient()
    runner = data.QueryRunner(client=client, settings=settings())
    frame = data.top_names(runner, "CA", 2000, 2010)
    assert list(frame["name"]) == ["Ada"]

    (dry_sql, dry_cfg), (sql, cfg) = client.calls
    assert dry_cfg.dry_run is True
    assert cfg.dry_run is not True
    assert cfg.maximum_bytes_billed == 10_000
    assert cfg.labels == {"workbench-app": "demo"}
    params = {p.name: p.value for p in cfg.query_parameters}
    assert params == {"state": "CA", "from_year": 2000, "to_year": 2010, "limit": 20}
    # User input is never interpolated into SQL.
    assert "CA" not in sql


def test_refuses_queries_over_the_byte_limit():
    client = FakeClient(bytes_processed=20_000)
    runner = data.QueryRunner(client=client, settings=settings())
    with pytest.raises(data.QueryTooExpensive):
        data.top_names(runner, "CA", 2000, 2010)
    assert len(client.calls) == 1  # only the free dry run happened


@pytest.mark.parametrize(
    "sql",
    ["DELETE FROM t WHERE true", "insert into t values (1)", "DROP TABLE t", "  update t set a=1"],
)
def test_only_select_statements_are_allowed(sql):
    runner = data.QueryRunner(client=FakeClient(), settings=settings())
    with pytest.raises(ValueError):
        runner.run(sql, [])


def test_allows_with_and_leading_comments():
    data.ensure_read_only("-- top names\nWITH x AS (SELECT 1) SELECT * FROM x")
    data.ensure_read_only("select 1")


def test_rejects_inverted_year_range():
    runner = data.QueryRunner(client=FakeClient(), settings=settings())
    with pytest.raises(ValueError):
        data.top_names(runner, "CA", 2010, 2000)


def test_settings_default_byte_limit(monkeypatch):
    monkeypatch.delenv("MAX_BYTES_BILLED", raising=False)
    assert data.Settings().max_bytes_billed == data.DEFAULT_MAX_BYTES_BILLED
    monkeypatch.setenv("MAX_BYTES_BILLED", "5")
    assert data.Settings().max_bytes_billed == 5
