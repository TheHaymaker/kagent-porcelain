.PHONY: install test lint run
PORT ?= 8080

install:
	uv sync --frozen

test: install
	uv run pytest

lint: install
	uv run ruff check .
	uv run ruff format --check .

# Local run without IAP: LOCAL_DEV_USER stands in for the signed-in user.
run: install
	LOCAL_DEV_USER=$${LOCAL_DEV_USER:-dev@example.com} uv run uvicorn app.main:app --reload --port $(PORT)
