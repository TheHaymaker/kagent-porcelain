# syntax=docker/dockerfile:1.7
FROM python:3.12-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.11.32 /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
WORKDIR /srv
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY app ./app
COPY .streamlit ./.streamlit

FROM python:3.12-slim
RUN useradd --system --uid 10001 --home-dir /srv app
WORKDIR /srv
COPY --from=build /srv /srv
ENV PATH="/srv/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PORT=8080 HOME=/srv
USER 10001
EXPOSE 8080
# Health: GET /_stcore/health (Streamlit built-in).
CMD ["sh", "-c", "exec streamlit run app/streamlit_app.py --server.port=${PORT} --server.address=0.0.0.0"]
