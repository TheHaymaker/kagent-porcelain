// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import App from '../web/src/App.jsx';

function fakeApi() {
  const items = [];
  return {
    me: async () => ({ email: 'alice@example.com' }),
    listItems: async () => items.map((i) => ({ ...i })),
    addItem: async (title) => {
      if (!title.trim()) throw new Error('title must not be empty');
      const item = { id: items.length + 1, title, createdBy: 'alice@example.com', done: false };
      items.push(item);
      return item;
    },
    toggleItem: async (id) => {
      const item = items.find((i) => i.id === id);
      item.done = !item.done;
      return item;
    },
  };
}

afterEach(cleanup);

describe('App', () => {
  it('shows the signed-in user and an empty list', async () => {
    render(<App api={fakeApi()} />);
    expect(await screen.findByText('Signed in as alice@example.com')).toBeTruthy();
    expect(screen.getByText('Nothing here yet.')).toBeTruthy();
  });

  it('adds and toggles an item', async () => {
    render(<App api={fakeApi()} />);
    await screen.findByText('Signed in as alice@example.com');
    fireEvent.change(screen.getByLabelText('New item'), { target: { value: 'Book room' } });
    await act(async () => fireEvent.click(screen.getByText('Add')));
    expect(await screen.findByText('Book room')).toBeTruthy();
    expect(screen.getByLabelText('New item').value).toBe('');

    await act(async () => fireEvent.click(screen.getByLabelText('Mark Book room as done')));
    expect(await screen.findByLabelText('Mark Book room as not done')).toBeTruthy();
  });

  it('shows API errors as an alert', async () => {
    render(<App api={fakeApi()} />);
    await screen.findByText('Signed in as alice@example.com');
    await act(async () => fireEvent.click(screen.getByText('Add')));
    expect((await screen.findByRole('alert')).textContent).toMatch(/must not be empty/);
  });
});
