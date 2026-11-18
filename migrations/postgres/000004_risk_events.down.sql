BEGIN;

DROP INDEX IF EXISTS idx_risk_events_code_ts;
DROP INDEX IF EXISTS idx_risk_events_account_ts;
DROP INDEX IF EXISTS idx_risk_events_ts;
DROP TABLE IF EXISTS risk_events;

COMMIT;
