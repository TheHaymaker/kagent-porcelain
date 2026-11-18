import { useEffect, useState } from 'react';
import { api as defaultApi } from './api.js';

export default function App({ api = defaultApi }) {
  const [me, setMe] = useState(null);
  const [items, setItems] = useState([]);
  const [title, setTitle] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    Promise.all([api.me(), api.listItems()])
      .then(([user, list]) => {
        setMe(user);
        setItems(list);
      })
      .catch((err) => setError(err.message));
  }, [api]);

  async function onSubmit(event) {
    event.preventDefault();
    setError('');
    try {
      await api.addItem(title);
      setTitle('');
      setItems(await api.listItems());
    } catch (err) {
      setError(err.message);
    }
  }

  async function onToggle(id) {
    try {
      await api.toggleItem(id);
      setItems(await api.listItems());
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <main>
      <header>
        <h1>workbench-app</h1>
        {me && <p className="who">Signed in as {me.email}</p>}
      </header>
      <form onSubmit={onSubmit}>
        <label htmlFor="title">New item</label>
        <input id="title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
        <button type="submit">Add</button>
      </form>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      <ul>
        {items.length === 0 && <li className="empty">Nothing here yet.</li>}
        {items.map((item) => (
          <li key={item.id} className={item.done ? 'done' : ''}>
            <button
              type="button"
              aria-label={`Mark ${item.title} as ${item.done ? 'not done' : 'done'}`}
              onClick={() => onToggle(item.id)}
            >
              {item.done ? '✓' : '○'}
            </button>
            <span className="title">{item.title}</span>
            <span className="meta">added by {item.createdBy}</span>
          </li>
        ))}
      </ul>
    </main>
  );
}
