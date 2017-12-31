DROP TABLE IF EXISTS utxo CASCADE;
DROP TABLE IF EXISTS payout_transaction CASCADE;
DROP TABLE IF EXISTS share CASCADE;
DROP TABLE IF EXISTS block CASCADE;
DROP TABLE IF EXISTS payout_address CASCADE;
DROP TABLE IF EXISTS credit CASCADE;
DROP TABLE IF EXISTS users CASCADE;
DROP TABLE IF EXISTS payout CASCADE;
DROP TYPE IF EXISTS block_status CASCADE;

CREATE TABLE share
(
    username varchar NOT NULL,
    difficulty double precision NOT NULL,
    mined_at timestamp NOT NULL,
    sharechain varchar NOT NULL,
    currencies varchar[] NOT NULL
);

CREATE TABLE utxo
(
    currency varchar NOT NULL,
    address varchar NOT NULL,
    hash varchar NOT NULL,
    vout integer NOT NULL,
    amount bigint NOT NULL,
    spent boolean NOT NULL DEFAULT false,
    spendable boolean NOT NULL DEFAULT false,
    CONSTRAINT utxo_pkey PRIMARY KEY (hash)
);

CREATE TYPE block_status AS ENUM ('immature', 'orphan', 'mature');
CREATE TABLE block
(
    currency varchar NOT NULL,
    powalgo varchar NOT NULL,
    height bigint NOT NULL,
    hash varchar NOT NULL,
    coinbase_hash varchar NOT NULL,
    powhash varchar NOT NULL,
    subsidy numeric NOT NULL,
    mined_at timestamp NOT NULL,
    mined_by varchar NOT NULL,
    target double precision NOT NULL,
    status block_status DEFAULT 'immature' NOT NULL,
    credited boolean DEFAULT false NOT NULL,
    payout_data json DEFAULT '{}'::JSON NOT NULL,
    CONSTRAINT block_pkey PRIMARY KEY (hash),
    CONSTRAINT coinbase_hash_fk FOREIGN KEY (coinbase_hash)
        REFERENCES utxo (hash) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION
);

CREATE TABLE users
(
    id SERIAL NOT NULL,
    username varchar,
    password varchar,
    email varchar,
    verified_email boolean NOT NULL DEFAULT false,
    tfa_code varchar,
    tfa_enabled boolean NOT NULL DEFAULT false,
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT unique_email UNIQUE (email),
    CONSTRAINT unique_username UNIQUE (username)
);

CREATE TABLE payout_transaction
(
    hash varchar NOT NULL,
    currency varchar,
    sent timestamp,
    signed_tx bytea NOT NULL,
    confirmed boolean NOT NULL DEFAULT false,
    CONSTRAINT payout_transaction_pkey PRIMARY KEY (hash)
);

CREATE TABLE payout
(
    user_id integer NOT NULL,
    amount bigint NOT NULL,
    payout_transaction varchar NOT NULL,
    fee integer NOT NULL,
    address varchar NOT NULL,
    CONSTRAINT payout_transaction_fkey FOREIGN KEY (payout_transaction)
        REFERENCES payout_transaction (hash) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT user_id_fk FOREIGN KEY (user_id)
        REFERENCES users (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION
);

CREATE TABLE payout_address
(
    user_id integer NOT NULL,
    currency varchar,
    address varchar,
    CONSTRAINT user_id_fk FOREIGN KEY (user_id)
        REFERENCES users (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT payout_address_pkey PRIMARY KEY (user_id, currency)
);

CREATE TABLE credit
(
    id SERIAL NOT NULL,
    user_id integer NOT NULL,
    amount numeric NOT NULL,
    currency varchar NOT NULL,
    blockhash varchar NOT NULL,
    sharechain varchar NOT NULL,
    payout_transaction varchar,
    CONSTRAINT payout_transaction_fkey FOREIGN KEY (payout_transaction)
        REFERENCES payout_transaction (hash) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT unique_credit UNIQUE (user_id, blockhash),
    CONSTRAINT blockhash_fk FOREIGN KEY (blockhash)
        REFERENCES block (hash) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT user_id_fk FOREIGN KEY (user_id)
        REFERENCES users (id) MATCH SIMPLE
        ON UPDATE NO ACTION
        ON DELETE NO ACTION,
    CONSTRAINT credit_pkey PRIMARY KEY (id)
);

INSERT INTO users (id, username, password, email, verified_email, tfa_code, tfa_enabled) VALUES (1, 'fee', '$2a$06$pJF0DSl6M7pTjPv8hBTP1uL/lAe7UqHZl5gKc3QA02yRFV1oCTFum', 'fee@test.com', false, NULL, false);
