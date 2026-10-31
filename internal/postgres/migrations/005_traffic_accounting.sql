ALTER TABLE node_traffic_state ADD COLUMN accounting_epoch uuid NOT NULL DEFAULT gen_random_uuid(), ADD COLUMN accounting_sequence bigint NOT NULL DEFAULT 0 CHECK (accounting_sequence >= 0);
