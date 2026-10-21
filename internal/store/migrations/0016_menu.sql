-- Cardápio: LiteZap's own catalog, kept here (never written to Meta/WhatsApp). Prices in
-- cents; images are files under data/menu/<instance>/<image_id>.jpg.
CREATE TABLE menu_categories (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX menu_categories_inst ON menu_categories (instance_id, position);

-- category_id '' is "no category" (deleting a category keeps its items).
CREATE TABLE menu_items (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  category_id TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  price_cents INTEGER NOT NULL DEFAULT 0,
  currency    TEXT NOT NULL DEFAULT 'BRL',
  image_id    TEXT NOT NULL DEFAULT '',
  available   INTEGER NOT NULL DEFAULT 1,
  position    INTEGER NOT NULL DEFAULT 0,
  sku         TEXT NOT NULL DEFAULT '',
  tags        TEXT NOT NULL DEFAULT '[]',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX menu_items_inst ON menu_items (instance_id, category_id, position);
-- Items copied from the WhatsApp catalog ("wa:<product id>") exist once per instance:
-- concurrent or repeated copies skip what is already there.
CREATE UNIQUE INDEX menu_items_wa_sku ON menu_items (instance_id, sku) WHERE sku LIKE 'wa:%';

-- An option group of an item ("Tamanho", "Borda"): choices with price deltas, as JSON
-- [{"name":"G","price_delta_cents":1000}].
CREATE TABLE menu_options (
  id          TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  item_id     TEXT NOT NULL REFERENCES menu_items(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  min_select  INTEGER NOT NULL DEFAULT 0,
  max_select  INTEGER NOT NULL DEFAULT 1,
  required    INTEGER NOT NULL DEFAULT 0,
  position    INTEGER NOT NULL DEFAULT 0,
  choices     TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX menu_options_item ON menu_options (item_id, position);
