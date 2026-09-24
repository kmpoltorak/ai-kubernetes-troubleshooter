CREATE TABLE incidents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title         text NOT NULL,
    description   text NOT NULL,
    cluster       text NOT NULL,
    namespace     text NOT NULL,
    resource_type text NOT NULL CHECK (resource_type IN ('deployment', 'service', 'pod')),
    resource_name text NOT NULL,
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'analyzed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incidents_created_at_idx ON incidents (created_at DESC);

CREATE TABLE investigations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id  uuid NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    status       text NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    scenario     text NOT NULL DEFAULT '',
    error        text NOT NULL DEFAULT '',
    started_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX investigations_incident_idx ON investigations (incident_id, started_at DESC);

CREATE TABLE tool_executions (
    id               uuid PRIMARY KEY,
    investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
    tool_name        text NOT NULL,
    status           text NOT NULL CHECK (status IN ('succeeded', 'failed')),
    input            jsonb NOT NULL,
    output           jsonb,
    error            text NOT NULL DEFAULT '',
    duration_ms      bigint NOT NULL,
    started_at       timestamptz NOT NULL
);
CREATE INDEX tool_executions_investigation_idx ON tool_executions (investigation_id);

CREATE TABLE evidence (
    id                uuid PRIMARY KEY,
    investigation_id  uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
    tool_execution_id uuid NOT NULL REFERENCES tool_executions (id) ON DELETE CASCADE,
    position          integer NOT NULL,
    source            text NOT NULL,
    subject           text NOT NULL,
    health            text NOT NULL CHECK (health IN ('healthy', 'degraded', 'down')),
    summary           text NOT NULL,
    signals           text[] NOT NULL DEFAULT '{}',
    data              jsonb NOT NULL
);
CREATE INDEX evidence_investigation_idx ON evidence (investigation_id, position);

CREATE TABLE reports (
    id               uuid PRIMARY KEY,
    investigation_id uuid NOT NULL UNIQUE REFERENCES investigations (id) ON DELETE CASCADE,
    summary          text NOT NULL,
    root_cause       text NOT NULL,
    confidence       numeric(3, 2) NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    severity         text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    analysis         jsonb NOT NULL,
    provider         text NOT NULL,
    created_at       timestamptz NOT NULL
);

CREATE TABLE remediation_actions (
    id                uuid PRIMARY KEY,
    report_id         uuid NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    position          integer NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('recommendation', 'diagnostic_command')),
    description       text NOT NULL,
    command           text NOT NULL DEFAULT '',
    requires_approval boolean NOT NULL,
    status            text NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'approved', 'rejected')),
    UNIQUE (report_id, position)
);
