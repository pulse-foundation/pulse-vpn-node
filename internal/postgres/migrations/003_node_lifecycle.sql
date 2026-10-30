-- The singleton connection and users share node_user_state's revision/lock.
-- SQL owns recoverable REALITY secrets after the one-time legacy file import.
ALTER TABLE node_user_state ADD COLUMN connection_initialized boolean NOT NULL DEFAULT false;
ALTER TABLE node_user_state ADD COLUMN connection_settings jsonb;
ALTER TABLE node_user_state ADD CONSTRAINT connection_settings_object CHECK (connection_settings IS NULL OR jsonb_typeof(connection_settings)='object');
ALTER TABLE node_users ADD COLUMN deleted boolean NOT NULL DEFAULT false;
ALTER TABLE node_users ADD COLUMN credential_revision bigint NOT NULL DEFAULT 1 CHECK (credential_revision>0);
-- Retain every retired credential and its runtime attribution. This prevents UUID
-- reuse and allows old sessions to be swept and counted after rotation/deletion.
CREATE TABLE node_user_credentials (
 uuid uuid PRIMARY KEY,
 user_name text NOT NULL REFERENCES node_users(name),
 runtime_name text NOT NULL UNIQUE CHECK (runtime_name ~ '^[a-zA-Z0-9_-]{1,64}$'),
 revoked boolean NOT NULL DEFAULT false
);
INSERT INTO node_user_credentials(uuid,user_name,runtime_name) SELECT uuid,name,name FROM node_users;
CREATE INDEX node_user_credentials_user_name ON node_user_credentials(user_name);
