"""workbench-app: a data app over BigQuery (Streamlit on Cloud Run behind IAP)."""

from __future__ import annotations

import logging
import sys
from pathlib import Path

# `streamlit run app/streamlit_app.py` puts app/ on sys.path, not the repo root.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import streamlit as st  # noqa: E402

from app import data, identity  # noqa: E402
from app.logging_config import configure_logging  # noqa: E402

APP_TITLE = "workbench-app"

configure_logging()
log = logging.getLogger("workbench-app")

st.set_page_config(page_title=APP_TITLE, layout="wide")

try:
    email = identity.user_email(st.context.headers)
except identity.NotSignedIn:
    st.error("You need to be signed in to use this app.")
    st.stop()


@st.cache_resource
def get_runner() -> data.QueryRunner:
    return data.QueryRunner()


@st.cache_data(ttl=600, show_spinner=False)
def load_top_names(state: str, from_year: int, to_year: int):
    return data.top_names(get_runner(), state, from_year, to_year)


st.title(APP_TITLE)
st.caption(f"Signed in as {email}")

STATES = ["CA", "NY", "TX", "WA", "IL", "FL"]
with st.sidebar:
    state = st.selectbox("State", STATES, index=0)
    from_year, to_year = st.slider("Years", 1910, 2023, (1990, 2023))

try:
    with st.spinner("Loading..."):
        frame = load_top_names(state, from_year, to_year)
except data.QueryTooExpensive:
    log.warning("query refused by cost limit", extra={"fields": {"state": state}})
    st.warning("That selection would read too much data. Try a narrower range.")
    st.stop()
except Exception:
    log.exception("query failed")
    st.error("The data could not be loaded. Please try again in a minute.")
    st.stop()

if frame.empty:
    st.info("No rows for this selection.")
else:
    col1, col2 = st.columns([2, 1])
    with col1:
        st.subheader(f"Most common names in {state}, {from_year}-{to_year}")
        st.bar_chart(frame, x="name", y="total", horizontal=True)
    with col2:
        st.dataframe(frame, hide_index=True, width="stretch")
        st.download_button(
            "Download CSV", frame.to_csv(index=False), file_name=f"top-names-{state}.csv"
        )
