-- +goose Up
-- Fungsi bersama untuk mengunci tabel ledger dan event menjadi append-only.
-- Dipasang sebagai trigger BEFORE UPDATE OR DELETE di migrasi tabel-tabel itu.
-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'table % is append-only', TG_TABLE_NAME
    USING ERRCODE = 'restrict_violation';
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS forbid_mutation();
