.PHONY: install test lint run
PORT ?= 8501

install:
	uv sync --frozen

test: install
	uv run pytest

lint: install
	uv run ruff check .
	uv run ruff format --check .

# Local run uses your own Application Default Credentials
# (gcloud auth application-default login) and BQ_PROJECT for billing.
run: install
	LOCAL_DEV_USER=$${LOCAL_DEV_USER:-dev@example.com} uv run streamlit run app/streamlit_app.py --server.port=$(PORT)
