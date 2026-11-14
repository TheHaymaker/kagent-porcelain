// Thin client for the Express API. Same origin, so IAP's session cookie is sent
// automatically; no tokens in the browser code.
async function request(path, options = {}) {
  const response = await fetch(`/api${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(body.error ?? `Request failed (${response.status})`);
  }
  return body;
}

export const api = {
  me: () => request('/me'),
  listItems: () => request('/items').then((b) => b.items),
  addItem: (title) => request('/items', { method: 'POST', body: JSON.stringify({ title }) }),
  toggleItem: (id) => request(`/items/${id}/toggle`, { method: 'POST' }),
};
