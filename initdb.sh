#!/bin/bash

set -e

POSTGRES_DB=${POSTGRES_DB}
POSTGRES_USER=${POSTGRES_USER}
TABLE_NAME=${TABLE_NAME}

psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<-EOSQL
  \c $POSTGRES_DB;

  CREATE TABLE $TABLE_NAME (
    id SERIAL PRIMARY KEY,
    client_ip VARCHAR(255),
    domain VARCHAR(255),
    created_at TIMESTAMP,
    addresses JSONB
  );

  GRANT ALL PRIVILEGES ON DATABASE "$POSTGRES_DB" TO "$POSTGRES_USER";
EOSQL

