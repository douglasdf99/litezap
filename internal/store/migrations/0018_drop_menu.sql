-- The Cardápio (LiteZap's own catalog, 0016 and 0017) was removed: LiteZap is a WhatsApp
-- API, not a menu system. The native WhatsApp catalog and its orders (0015) stay.

-- Attendant profiles that used the menu as their product list (knowledge_source "menu")
-- fall back to the WhatsApp catalog when the instance clearly has one (its menu was
-- copied from it, or it received catalog orders), and to the typed products otherwise.
UPDATE attendant_agents
   SET profile_json = json_set(profile_json, '$.knowledge_source',
         CASE WHEN EXISTS (SELECT 1 FROM menu_items m WHERE m.instance_id = attendant_agents.instance_id AND m.source_ref LIKE 'wa:%')
                OR EXISTS (SELECT 1 FROM orders o WHERE o.instance_id = attendant_agents.instance_id)
              THEN 'catalog' ELSE 'manual' END)
 WHERE json_valid(profile_json) AND lower(trim(COALESCE(json_extract(profile_json, '$.knowledge_source'), ''))) = 'menu';

-- Children first (foreign keys are on).
DROP TABLE IF EXISTS menu_collection_items;
DROP TABLE IF EXISTS menu_item_images;
DROP TABLE IF EXISTS menu_options;
DROP TABLE IF EXISTS menu_collections;
DROP TABLE IF EXISTS menu_items;
DROP TABLE IF EXISTS menu_categories;
DROP TABLE IF EXISTS order_drafts;
