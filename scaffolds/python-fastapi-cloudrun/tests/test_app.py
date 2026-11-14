from tests.conftest import IAP_ALICE


def test_healthz_needs_no_sign_in(client):
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_pages_require_sign_in(client):
    assert client.get("/").status_code == 401
    assert client.post("/items", data={"title": "x"}).status_code == 401


def test_index_shows_signed_in_user(client):
    response = client.get("/", headers=IAP_ALICE)
    assert response.status_code == 200
    assert "Signed in as alice@example.com" in response.text
    assert "Nothing here yet." in response.text
    assert response.headers["Content-Security-Policy"].startswith("default-src 'self'")


def test_add_item_records_who_added_it(client):
    response = client.post("/items", data={"title": "Order toner"}, headers=IAP_ALICE)
    assert response.status_code == 200
    assert "Order toner" in response.text
    assert "added by alice@example.com" in response.text
    assert 'id="board"' in response.text


def test_add_item_rejects_blank_title_inline(client):
    response = client.post("/items", data={"title": "   "}, headers=IAP_ALICE)
    assert response.status_code == 200
    assert 'role="alert"' in response.text
    assert "must not be empty" in response.text


def test_item_titles_are_html_escaped(client):
    response = client.post("/items", data={"title": "<script>x</script>"}, headers=IAP_ALICE)
    assert "<script>x</script>" not in response.text
    assert "&lt;script&gt;" in response.text


def test_toggle_marks_item_done(client):
    client.post("/items", data={"title": "Book room"}, headers=IAP_ALICE)
    response = client.post("/items/1/toggle", headers=IAP_ALICE)
    assert response.status_code == 200
    assert 'class="done"' in response.text


def test_toggle_unknown_item_is_404(client):
    assert client.post("/items/99/toggle", headers=IAP_ALICE).status_code == 404


def test_static_htmx_is_served(client):
    response = client.get("/static/htmx.min.js")
    assert response.status_code == 200
    assert "htmx" in response.text[:200]
