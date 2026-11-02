CREATE TABLE investigation_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    idempotency_key uuid NOT NULL,
    requested_scenario text NOT NULL,
    scenario text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed')),
    stage text NOT NULL DEFAULT 'queued' CHECK(stage IN ('queued','diagnostics','analyzing','completed','failed')),
    attempts integer NOT NULL DEFAULT 1 CHECK(attempts BETWEEN 1 AND 3),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deadline_at timestamptz NOT NULL,
    next_run_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    error text NOT NULL DEFAULT '',
    lease_token uuid,
    lease_until timestamptz,
    UNIQUE(incident_id,idempotency_key),
    UNIQUE(id,incident_id),
    CHECK((status='running') = (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX jobs_pending_idx ON investigation_jobs(next_run_at,created_at,id) WHERE status='queued';
CREATE INDEX jobs_running_idx ON investigation_jobs(lease_until,deadline_at) WHERE status='running';
ALTER TABLE investigations DROP CONSTRAINT investigations_status_check;
ALTER TABLE investigations ADD CONSTRAINT investigations_status_check CHECK(status IN ('queued','running','completed','failed'));
ALTER TABLE investigations ADD COLUMN job_id uuid;
ALTER TABLE investigations ADD COLUMN execution_attempt integer NOT NULL DEFAULT 0;
ALTER TABLE investigations ADD CONSTRAINT investigations_job_fk FOREIGN KEY(job_id,incident_id) REFERENCES investigation_jobs(id,incident_id) ON DELETE CASCADE;
ALTER TABLE investigations ADD CONSTRAINT investigations_job_attempt_check CHECK((job_id IS NULL AND execution_attempt=0) OR (job_id IS NOT NULL AND execution_attempt BETWEEN 1 AND 3));
CREATE UNIQUE INDEX investigations_job_attempt_idx ON investigations(job_id,execution_attempt);
