DROP TABLE IF EXISTS cost_anomalies;
DROP FUNCTION IF EXISTS cloudoptix_ensure_cost_records_partition(date);
DROP TABLE IF EXISTS cost_records; -- cascades to every monthly + default partition
