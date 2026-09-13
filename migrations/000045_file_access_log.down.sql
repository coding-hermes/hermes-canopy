-- 000045_file_access_log.down.sql
DROP TRIGGER IF EXISTS trg_file_access_log_no_delete ON file_access_log;
DROP TRIGGER IF EXISTS trg_file_access_log_no_update ON file_access_log;
DROP FUNCTION IF EXISTS reject_file_access_log_mutation();
DROP TABLE IF EXISTS file_access_log;
