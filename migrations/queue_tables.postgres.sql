CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS
$$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$
LANGUAGE plpgsql;

DROP TABLE IF EXISTS jobs CASCADE;

CREATE TABLE jobs (
    id SERIAL PRIMARY KEY,
    job_id UUID NOT NULL,
    payload TEXT,
    attempts INT,
    name TEXT,
    reserved_at TIMESTAMP WITHOUT TIME ZONE,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_job_id ON jobs (job_id);
CREATE INDEX idx_jobs_name ON jobs (name);

CREATE TRIGGER set_timestamp_jobs
BEFORE UPDATE ON jobs
FOR EACH ROW
EXECUTE PROCEDURE trigger_set_timestamp();

DROP TABLE IF EXISTS failed_jobs CASCADE;

CREATE TABLE failed_jobs (
    id SERIAL PRIMARY KEY,
    job_id UUID NOT NULL,
    name TEXT,
    attempts INT,
    payload TEXT,
    exception TEXT,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT now()
);

CREATE INDEX idx_failed_jobs_job_id ON failed_jobs (job_id);
CREATE INDEX idx_failed_jobs_name ON failed_jobs (name);

CREATE TRIGGER set_timestamp_failed_jobs
BEFORE UPDATE ON failed_jobs
FOR EACH ROW
EXECUTE PROCEDURE trigger_set_timestamp();
