from pathlib import Path

import pandas as pd
import pytest
import streamlit as st
from streamlit.testing.v1 import AppTest

from app import data

APP = str(Path(__file__).resolve().parent.parent / "app" / "streamlit_app.py")


@pytest.fixture(autouse=True)
def _isolate(monkeypatch):
    for name in ("IAP_AUDIENCE", "K_SERVICE", "LOCAL_DEV_USER"):
        monkeypatch.delenv(name, raising=False)
    st.cache_data.clear()
    st.cache_resource.clear()


def fake_top_names(runner, state, from_year, to_year, limit=20):
    return pd.DataFrame({"name": ["Ada", "Grace"], "total": [30, 20]})


def test_requires_sign_in():
    at = AppTest.from_file(APP).run()
    assert at.error[0].value == "You need to be signed in to use this app."


def test_shows_chart_and_table_for_signed_in_user(monkeypatch):
    monkeypatch.setenv("LOCAL_DEV_USER", "dev@example.com")
    monkeypatch.setattr(data, "top_names", fake_top_names)
    at = AppTest.from_file(APP).run()
    assert not at.exception
    assert "Signed in as dev@example.com" in at.caption[0].value
    assert at.subheader[0].value.startswith("Most common names in CA")
    assert len(at.dataframe) == 1


def test_friendly_message_when_query_too_expensive(monkeypatch):
    monkeypatch.setenv("LOCAL_DEV_USER", "dev@example.com")

    def too_expensive(*args, **kwargs):
        raise data.QueryTooExpensive("big")

    monkeypatch.setattr(data, "top_names", too_expensive)
    at = AppTest.from_file(APP).run()
    assert "too much data" in at.warning[0].value


def test_changing_state_reruns_query(monkeypatch):
    monkeypatch.setenv("LOCAL_DEV_USER", "dev@example.com")
    seen = []

    def record(runner, state, from_year, to_year, limit=20):
        seen.append(state)
        return fake_top_names(runner, state, from_year, to_year)

    monkeypatch.setattr(data, "top_names", record)
    at = AppTest.from_file(APP).run()
    at.sidebar.selectbox[0].set_value("NY").run()
    assert seen == ["CA", "NY"]
