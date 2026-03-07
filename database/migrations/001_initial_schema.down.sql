-- 001_initial_schema.down.sql
-- Reverses 001_initial_schema.up.sql.
-- Drops tables in reverse dependency order (exec_sessions references containers).

DROP INDEX IF EXISTS idx_exec_sessions_status;
DROP INDEX IF EXISTS idx_exec_sessions_container_id;
DROP TABLE IF EXISTS exec_sessions;

DROP INDEX IF EXISTS idx_builds_status;
DROP TABLE IF EXISTS builds;

DROP INDEX IF EXISTS idx_networks_origin;
DROP INDEX IF EXISTS idx_networks_status;
DROP TABLE IF EXISTS networks;

DROP INDEX IF EXISTS idx_volumes_origin;
DROP INDEX IF EXISTS idx_volumes_status;
DROP TABLE IF EXISTS volumes;

DROP INDEX IF EXISTS idx_images_origin;
DROP INDEX IF EXISTS idx_images_status;
DROP TABLE IF EXISTS images;

DROP INDEX IF EXISTS idx_containers_origin;
DROP INDEX IF EXISTS idx_containers_status;
DROP TABLE IF EXISTS containers;
