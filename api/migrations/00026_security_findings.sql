-- +goose Up
-- Security findings (BE-9.4, F-11.4).
--
-- A finding is a probe that succeeded — a payload that reached its mark. Each is stored
-- against the run_result the probe produced, so a finding is a failed result with
-- security detail attached, and the promote-to-defect path that already works for a
-- failed test works for a finding unchanged (BE-9.4.2). What the extra row carries is
-- what a security finding needs and a test failure does not: a severity, the evidence
-- the detection rule matched, and reproduction steps precise enough that somebody can
-- confirm it by hand.

CREATE TABLE security_findings (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_result_id uuid NOT NULL REFERENCES run_results (id) ON DELETE CASCADE,
    run_id        uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,

    -- The reviewed library entry this finding came from, so a finding is always
    -- traceable to a payload a person approved rather than one a model invented.
    payload_id text NOT NULL,
    category   text NOT NULL,

    endpoint  text NOT NULL,
    parameter text NOT NULL DEFAULT '',

    severity text NOT NULL,

    -- What the detection rule matched: the reflected markup, the database error, the
    -- unexpected 2xx. This is the evidence a reviewer reads to agree it is real.
    evidence text NOT NULL DEFAULT '',

    -- Reproduction as a request a person can replay: method, URL, and how the payload
    -- was delivered, with the credential redacted.
    reproduction text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX security_findings_run_idx ON security_findings (run_id, severity);
CREATE UNIQUE INDEX security_findings_result_idx ON security_findings (run_result_id);

-- +goose Down
DROP TABLE IF EXISTS security_findings;
