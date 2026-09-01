-- The tables predate this migration; rollback removes only Phase 3 seed metadata.
UPDATE transport_configs SET config_json = config_json - 'priority' WHERE transport_type IN ('sse','nats');
