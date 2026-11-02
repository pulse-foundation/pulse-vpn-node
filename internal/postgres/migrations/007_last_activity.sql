-- Historical byte totals cannot establish when activity occurred. Keep unknown
-- dates NULL until a new positive traffic delta is committed.
ALTER TABLE node_traffic_totals ADD COLUMN last_activity_at timestamptz;
