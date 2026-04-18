CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS
$$
BEGIN
	NEW.updated_at = NOW();
	RETURN NEW;
END;
$$
LANGUAGE
plpgsql;

drop table if exists jobs cascade;

CREATE TABLE jobs (
    id SERIAL PRIMARY KEY,
    job_id UUID not null,
    payload TEXT,
    attempts INT,
    name TEXT,
    reserved_at timestamp without time zone,
    created_at timestamp without time zone NOT NULL DEFAULT now(),
    updated_at timestamp without time zone NOT NULL DEFAULT now()
);

drop table if exists failed_jobs cascade;
CREATE TABLE failed_jobs (
    id SERIAL PRIMARY KEY,
    job_id UUID not null,
    name TEXT,
    attempts int,
    payload TEXT,
    exception TEXT,
    created_at timestamp without time zone NOT NULL DEFAULT now(),
    updated_at timestamp without time zone NOT NULL DEFAULT now()
);
