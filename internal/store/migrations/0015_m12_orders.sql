-- Marco 12 (catalog phase 1): orders received from the WhatsApp catalog. The token
-- authorizes reading the order details and never leaves the server.
CREATE TABLE orders (
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  order_id      TEXT NOT NULL,
  message_id    TEXT NOT NULL DEFAULT '',
  wa_message_id TEXT NOT NULL DEFAULT '',
  chat_jid      TEXT NOT NULL DEFAULT '',
  seller_jid    TEXT NOT NULL DEFAULT '',
  token         TEXT NOT NULL DEFAULT '',
  item_count    INTEGER NOT NULL DEFAULT 0,
  total_1000    INTEGER NOT NULL DEFAULT 0,
  currency      TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '',
  note          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT '',
  from_me       INTEGER NOT NULL DEFAULT 0,
  received_at   TEXT NOT NULL,
  stored_at     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (instance_id, order_id)
);
CREATE INDEX orders_recent ON orders (instance_id, received_at);
CREATE INDEX orders_stored ON orders (instance_id, stored_at);
