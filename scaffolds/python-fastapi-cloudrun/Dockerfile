# syntax=docker/dockerfile:1.7
# Build stage: resolve exactly the locked runtime dependencies.
FROM python:3.12-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.11.32 /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
WORKDIR /srv
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY app ./app

# Runtime stage: no build tools, non-root.
FROM python:3.12-slim
RUN useradd --system --uid 10001 --no-create-home app
WORKDIR /srv
COPY --from=build /srv /srv
ENV PATH="/srv/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PORT=8080
USER 10001
EXPOSE 8080
# Cloud Run sets PORT. Request logs come from the app (structured JSON), so
# uvicorn's access log is off.
CMD ["sh", "-c", "exec uvicorn app.main:app --host 0.0.0.0 --port ${PORT} --proxy-headers --forwarded-allow-ips='*' --no-access-log"]
