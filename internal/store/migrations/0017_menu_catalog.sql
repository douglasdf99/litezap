-- Cardápio, parity with the WhatsApp catalog (still never written to Meta/WhatsApp):
-- sale prices, link, hidden items, several photos per item, collections and the order
-- drafts built in a chat.

-- sale_price_cents 0 = no sale; sale_until '' = no end date (YYYY-MM-DD, last day of
-- the sale). hidden items are never shown to customers, the attendant or the agents
-- (available=0 is "esgotado": still listed, marked sold out). source_ref identifies an
-- item copied from elsewhere ("wa:<product id>" for the WhatsApp catalog), so a copy is
-- never repeated; until now that was the sku, which is free again (retailer_id).
ALTER TABLE menu_items ADD COLUMN sale_price_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE menu_items ADD COLUMN sale_until TEXT NOT NULL DEFAULT '';
ALTER TABLE menu_items ADD COLUMN link_url TEXT NOT NULL DEFAULT '';
ALTER TABLE menu_items ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0;
ALTER TABLE menu_items ADD COLUMN source_ref TEXT NOT NULL DEFAULT '';
UPDATE menu_items SET source_ref = sku WHERE sku LIKE 'wa:%';
CREATE UNIQUE INDEX menu_items_source ON menu_items (instance_id, source_ref) WHERE source_ref != '';

-- The photos of an item, in order (position 0 is the main photo, mirrored in
-- menu_items.image_id). Files are data/menu/<instance>/<id>.jpg. source_url is where a
-- photo was downloaded from ('' for an upload), so the CSV export can list it.
CREATE TABLE menu_item_images (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  item_id     TEXT NOT NULL REFERENCES menu_items(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL DEFAULT 0,
  source_url  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX menu_item_images_item ON menu_item_images (instance_id, item_id, position);
INSERT INTO menu_item_images (id, instance_id, item_id, position, source_url, created_at)
  SELECT image_id, instance_id, id, 0, '', updated_at FROM menu_items WHERE image_id != '';

-- Collections ("Promoções", "Mais vendidos"): many-to-many with items, independent of
-- the categories. cover_image_id is a photo of one of the menu's items ('' = none).
CREATE TABLE menu_collections (
  id             TEXT PRIMARY KEY,
  instance_id    TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  position       INTEGER NOT NULL DEFAULT 0,
  active         INTEGER NOT NULL DEFAULT 1,
  cover_image_id TEXT NOT NULL DEFAULT '',
  source_ref     TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX menu_collections_inst ON menu_collections (instance_id, position);
CREATE UNIQUE INDEX menu_collections_source ON menu_collections (instance_id, source_ref) WHERE source_ref != '';

CREATE TABLE menu_collection_items (
  collection_id TEXT NOT NULL REFERENCES menu_collections(id) ON DELETE CASCADE,
  item_id       TEXT NOT NULL REFERENCES menu_items(id) ON DELETE CASCADE,
  instance_id   TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  position      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (collection_id, item_id)
);
CREATE INDEX menu_collection_items_item ON menu_collection_items (instance_id, item_id);

-- An order built in a chat (cart): the lines priced by the engine (options, sale
-- prices, availability), as JSON, and its totals in cents. source: api, mcp, dashboard
-- or attendant (the Atendente IA's agent.order_draft). valid = 0 when a line has a
-- problem (an item out of the menu, sold out, a missing required option).
CREATE TABLE order_drafts (
  id             TEXT PRIMARY KEY,
  instance_id    TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  chat           TEXT NOT NULL DEFAULT '',
  customer_name  TEXT NOT NULL DEFAULT '',
  address        TEXT NOT NULL DEFAULT '',
  notes          TEXT NOT NULL DEFAULT '',
  items_json     TEXT NOT NULL DEFAULT '[]',
  subtotal_cents INTEGER NOT NULL DEFAULT 0,
  total_cents    INTEGER NOT NULL DEFAULT 0,
  currency       TEXT NOT NULL DEFAULT 'BRL',
  valid          INTEGER NOT NULL DEFAULT 1,
  status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sent', 'confirmed', 'cancelled')),
  source         TEXT NOT NULL DEFAULT 'api',
  message_id     TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX order_drafts_inst ON order_drafts (instance_id, created_at);
CREATE INDEX order_drafts_chat ON order_drafts (instance_id, chat, status);
