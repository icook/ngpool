DROP INDEX chain_time_key;
DROP TABLE public.share;
DROP TABLE public.block;

CREATE TABLE public.share
(
    username varchar NOT NULL,
    difficulty double precision NOT NULL,
    mined_at timestamp NOT NULL,
    chain varchar NOT NULL
);

CREATE INDEX chain_time_key
    ON public.share USING btree
    (chain, mined_at)
    TABLESPACE pg_default;

CREATE TABLE public.block
(
    currency varchar NOT NULL,
    height bigint NOT NULL,
    blockhash varchar NOT NULL,
    powhash varchar NOT NULL,
    subsidy numeric NOT NULL,
    mined_at timestamp NOT NULL,
    mined_by varchar NOT NULL,
    difficulty double precision NOT NULL,
    chain varchar NOT NULL
);
