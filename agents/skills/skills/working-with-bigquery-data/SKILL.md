---
name: working-with-bigquery-data
description: How to find, inspect and query company data in BigQuery safely for an app - using the bigquery tools to look at schemas and dry-run cost, writing parameterised read-only queries, wiring them into the streamlit-bigquery scaffold with maximum bytes billed, and what to tell the requester about data access. Use whenever an app reads data that already exists in BigQuery.
---

# Working with BigQuery data

Apps read data; they never write it. You look at data through the
`bigquery-mcp` tools (read-only, with a server-side bytes limit); the app reads
it at runtime as its own read-only service account.

## 1. Find the data

- Ask the requester where the data lives in their words ("the weekly sales
  report", "the support tickets table"). If they don't know, list datasets and
  tables with the bigquery tools and suggest the likely one by name and
  description; confirm with them before using it.
- Read the table schema and description. Look at column names, types,
  partitioning (`_PARTITIONDATE`, a date column) and clustering.
- Never pull rows into the conversation beyond a handful needed to understand
  the shape (`LIMIT 5`), and never personal data. Summarise, don't paste.

## 2. Write the query

- `SELECT` only the columns the app shows. No `SELECT *`.
- Filter on the partition column whenever the table is partitioned; the app's
  controls (date range) should map to it.
- Every value the user chooses is a **query parameter** (`@state`, `@from_date`),
  never string formatting. Table names cannot be parameters; they are fixed in
  code.
- Dry-run the query with `bigquery_run_query` in dry-run mode (or the tool's
  estimate) for the widest range the app allows. If it would scan more than
  about 1 GiB, narrow it: tighter partitions, fewer columns, an aggregated
  table, or ask the data owner for a smaller view.
- Aggregate in SQL, not in pandas, when the result would otherwise be large.

## 3. Put it in the app (streamlit-bigquery)

- Add the SQL as a constant next to `TOP_NAMES_SQL` in `app/data.py`, with a
  small function that builds the `QueryParam` list, like `top_names()`.
- Call it through `QueryRunner.run`, which enforces SELECT-only, dry-runs,
  applies `maximum_bytes_billed` and labels the job. Don't create a
  `bigquery.Client` anywhere else.
- Wrap the loader in `@st.cache_data(ttl=600)` (or longer for daily data).
- Write a test with the fake client (see `tests/test_data.py`) that asserts the
  parameters and that user input does not appear in the SQL text.
- Set `MAX_BYTES_BILLED` for the preview deploy (default 1 GiB). Only raise it
  with a reason the requester agreed to, and never above 10 GiB.

## 4. Access

The app's runtime account `app-<name>@` needs `roles/bigquery.dataViewer` on the
dataset(s) it reads and `roles/bigquery.jobUser` on `BQ_PROJECT`. You cannot
grant these. Record them in `IAM.md`, mention them in the promotion note, and
tell the requester plainly: "The app will need permission to read <dataset>;
the data owner approves that as part of going live."

If the data is sensitive (personal, financial, HR), say so in the promotion
note and prefer an authorized view that exposes only what the app shows.
