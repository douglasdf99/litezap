-- Marco 7: webhook filters (by chat, message type and from_me), stored as JSON.
ALTER TABLE webhooks ADD COLUMN filters_json TEXT NOT NULL DEFAULT '';
