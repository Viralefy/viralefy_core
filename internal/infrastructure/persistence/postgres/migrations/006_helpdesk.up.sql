-- Helpdesk: tickets de suporte com thread de mensagens.
-- status: open | pending (aguardando cliente) | resolved | closed
-- priority: low | normal | high | urgent
-- author_type: user | admin
CREATE TABLE IF NOT EXISTS tickets (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id),
    subject            TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'open',
    priority           TEXT NOT NULL DEFAULT 'normal',
    order_id           TEXT REFERENCES orders(id),
    assigned_admin_id  TEXT REFERENCES admins(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ticket_messages (
    id           TEXT PRIMARY KEY,
    ticket_id    TEXT NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author_type  TEXT NOT NULL,
    author_id    TEXT NOT NULL,
    body         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tickets_user      ON tickets(user_id);
CREATE INDEX IF NOT EXISTS idx_tickets_status    ON tickets(status);
CREATE INDEX IF NOT EXISTS idx_tickets_updated   ON tickets(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_ticket_msgs_thread ON ticket_messages(ticket_id, created_at);
