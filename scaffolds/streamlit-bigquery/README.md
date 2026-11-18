# workbench-app

A data app built from the **streamlit-bigquery** golden path: Streamlit on
Cloud Run behind IAP, reading BigQuery through a read-only service account
with a hard per-query cost ceiling.

The example page charts the most common baby names per US state from the
public `bigquery-public-data.usa_names` dataset. Replace `TOP_NAMES_SQL` /
`top_names()` in `app/data.py` and the page in `app/streamlit_app.py`.

## Run it

```sh
make test   # uses fakes; no Google credentials needed
make run    # needs `gcloud auth application-default login` and BQ_PROJECT=<billing project>
make lint
```

## Layout

| Path | What it is |
| --- | --- |
| `app/streamlit_app.py` | The page: sign-in check, controls, chart, table, CSV download |
| `app/data.py` | `QueryRunner`: read-only check, dry run, `maximum_bytes_billed`, labels, parameters |
| `app/identity.py` | Signed-in user from IAP headers (`st.context.headers`); JWT verification with `IAP_AUDIENCE` |
| `app/logging_config.py` | JSON lines for Cloud Logging |
| `.streamlit/config.toml` | Headless server, XSRF on, usage stats off, error details hidden |
| `tests/` | pytest + Streamlit `AppTest` |
| `Dockerfile`, `.gitlab-ci.yml`, `IAM.md`, `scaffold.yaml` | Build, CI (no deploy), permissions, golden-path manifest |

## Rules for queries

- Use `QueryRunner.run(sql, [QueryParam(...)])`; never build SQL with f-strings.
- Select only the columns the page shows.
- Wrap loaders in `@st.cache_data(ttl=...)` so reruns do not re-query.

## Configuration

`BQ_PROJECT`, `BQ_LOCATION`, `MAX_BYTES_BILLED`, `APP_NAME`, `IAP_AUDIENCE`,
`LOCAL_DEV_USER` (local only), `LOG_LEVEL`. Health check: `GET /_stcore/health`.
On Cloud Run enable session affinity and a 3600s request timeout (Streamlit
uses a websocket per browser session).
