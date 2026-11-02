LOCK TABLE investigation_jobs IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM investigation_jobs WHERE status IN ('queued','running')) THEN
  RAISE EXCEPTION 'cannot downgrade with active jobs; drain the queue and stop workers first';
 END IF;
END $$;
ALTER TABLE investigations DROP CONSTRAINT investigations_job_fk;
ALTER TABLE investigations DROP CONSTRAINT investigations_job_attempt_check;
DROP INDEX investigations_job_attempt_idx;
ALTER TABLE investigations DROP COLUMN job_id;
ALTER TABLE investigations DROP COLUMN execution_attempt;
ALTER TABLE investigations DROP CONSTRAINT investigations_status_check;
ALTER TABLE investigations ADD CONSTRAINT investigations_status_check CHECK(status IN ('running','completed','failed'));
DROP TABLE investigation_jobs;
