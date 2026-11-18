"""workbench-app: an internal web tool (FastAPI + Jinja2 + HTMX on Cloud Run)."""

from __future__ import annotations

import logging
import os
import time
from pathlib import Path
from typing import Annotated

from fastapi import Depends, FastAPI, Form, HTTPException, Request
from fastapi.responses import HTMLResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from app.identity import User, current_user
from app.logging_config import configure_logging, trace_context, trace_from_headers
from app.store import Store, make_store

APP_NAME = "workbench-app"
BASE_DIR = Path(__file__).resolve().parent

configure_logging()
log = logging.getLogger(APP_NAME)

# Module level so FastAPI can resolve it from the string annotations.
UserDep = Annotated[User, Depends(current_user)]


def create_app(store: Store | None = None) -> FastAPI:
    app = FastAPI(title=APP_NAME, docs_url=None, redoc_url=None, openapi_url=None)
    app.state.store = store or make_store(APP_NAME)
    templates = Jinja2Templates(directory=BASE_DIR / "templates")
    app.mount("/static", StaticFiles(directory=BASE_DIR / "static"), name="static")
    project_id = os.environ.get("GOOGLE_CLOUD_PROJECT")

    @app.middleware("http")
    async def request_logging(request: Request, call_next):
        token = trace_context.set(trace_from_headers(request.headers, project_id))
        start = time.perf_counter()
        try:
            response = await call_next(request)
        except Exception:
            log.exception("unhandled error", extra={"fields": {"path": request.url.path}})
            response = JSONResponse({"detail": "Something went wrong"}, status_code=500)
        latency = time.perf_counter() - start
        if request.url.path != "/healthz":
            log.info(
                f"{request.method} {request.url.path} {response.status_code}",
                extra={
                    "fields": {
                        "httpRequest": {
                            "requestMethod": request.method,
                            "requestUrl": request.url.path,
                            "status": response.status_code,
                            "latency": f"{latency:.6f}s",
                            "userAgent": request.headers.get("user-agent", ""),
                        }
                    }
                },
            )
        trace_context.reset(token)
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["X-Frame-Options"] = "DENY"
        response.headers["Content-Security-Policy"] = (
            "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:"
        )
        return response

    @app.get("/healthz", include_in_schema=False)
    def healthz() -> dict:
        # Liveness only: no dependencies, so a dependency outage does not
        # make Cloud Run restart healthy instances.
        return {"status": "ok"}

    @app.get("/", response_class=HTMLResponse)
    def index(request: Request, user: UserDep):
        return templates.TemplateResponse(
            request,
            "index.html",
            {"user": user, "items": app.state.store.list(), "app_name": APP_NAME},
        )

    def board(request: Request, *, error: str | None = None, title: str = ""):
        # HTMX swaps only 2xx responses by default, so validation errors are
        # rendered into the board with 200 and announced via role="alert".
        return templates.TemplateResponse(
            request,
            "_board.html",
            {"items": app.state.store.list(), "error": error, "title": title},
        )

    @app.post("/items", response_class=HTMLResponse)
    def add_item(request: Request, user: UserDep, title: Annotated[str, Form()] = ""):
        try:
            item = app.state.store.add(title, created_by=user.email)
        except ValueError as exc:
            return board(request, error=str(exc), title=title)
        log.info("item added", extra={"fields": {"item_id": item.id}})
        return board(request)

    @app.post("/items/{item_id}/toggle", response_class=HTMLResponse)
    def toggle_item(request: Request, item_id: str, user: UserDep):
        try:
            app.state.store.toggle(item_id)
        except KeyError:
            raise HTTPException(status_code=404, detail="No such item") from None
        return board(request)

    return app


app = create_app()
