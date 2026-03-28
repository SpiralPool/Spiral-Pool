#!/bin/bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
# Spiral Pool Database Initialization
# This script runs automatically when the PostgreSQL container starts for the first time.
# V2.0.0-PHI_HASH_REACTOR with Merge Mining (AuxPoW) Support
#
# NOTE: The Go pool application creates pool-specific tables via migrations
# (e.g., shares_btc_regtest, blocks_btc_regtest). This file provides base
# tables for reference and Docker initialization. Column names use NO
# underscores to match the Go code (poolid, blockheight, confirmationprogress).
#
# Uses $POSTGRES_USER from docker-compose environment (matches DB_USER in .env).
# GRANT_USER overrides the GRANT target when the psql connection user differs from
# the application user (e.g., Patroni post-bootstrap connects as postgres superuser
# but grants must go to the spiralstratum app user).

set -e

GRANT_USER="${GRANT_USER:-${POSTGRES_USER}}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    -- Create extensions
    CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

    -- Shares table (template - Go creates pool-specific tables)
    CREATE TABLE IF NOT EXISTS shares (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        blockheight BIGINT NOT NULL CHECK (blockheight >= 0),
        difficulty DOUBLE PRECISION NOT NULL CHECK (difficulty > 0),
        networkdifficulty DOUBLE PRECISION NOT NULL CHECK (networkdifficulty > 0),
        miner VARCHAR(256) NOT NULL,
        worker VARCHAR(256),
        useragent VARCHAR(256),
        ipaddress VARCHAR(64),
        source VARCHAR(32),
        created TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );

    -- Blocks table (template - Go creates pool-specific tables)
    -- CRITICAL: UNIQUE constraint on (poolid, coin, hash) prevents duplicate block records
    -- which could cause accounting issues during network retries or HA failover
    CREATE TABLE IF NOT EXISTS blocks (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        coin VARCHAR(32) DEFAULT 'PRIMARY',
        blockheight BIGINT NOT NULL CHECK (blockheight >= 0),
        networkdifficulty DOUBLE PRECISION NOT NULL CHECK (networkdifficulty > 0),
        status VARCHAR(32) NOT NULL DEFAULT 'pending',
        type VARCHAR(32),
        confirmationprogress DOUBLE PRECISION DEFAULT 0 CHECK (confirmationprogress >= 0 AND confirmationprogress <= 1),
        effort DOUBLE PRECISION CHECK (effort >= 0),
        transactionconfirmationdata TEXT,
        miner VARCHAR(256),
        reward NUMERIC(28,12) CHECK (reward >= 0),
        source VARCHAR(64),
        hash VARCHAR(128),
        created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
        orphan_mismatch_count INTEGER DEFAULT 0 CHECK (orphan_mismatch_count >= 0),
        stability_check_count INTEGER DEFAULT 0 CHECK (stability_check_count >= 0),
        last_verified_tip VARCHAR(64) DEFAULT '',
        submitted_node VARCHAR(50),
        submission_attempts INTEGER DEFAULT 1 CHECK (submission_attempts >= 1),
        CONSTRAINT blocks_unique_hash UNIQUE (poolid, coin, hash)
    );

    -- Auxiliary blocks table for merge mining (AuxPoW)
    -- Stores blocks found on auxiliary chains through merge mining
    CREATE TABLE IF NOT EXISTS aux_blocks (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        coin VARCHAR(32) NOT NULL,
        chainid INT NOT NULL CHECK (chainid >= 0),
        blockheight BIGINT NOT NULL CHECK (blockheight >= 0),
        networkdifficulty DOUBLE PRECISION NOT NULL CHECK (networkdifficulty > 0),
        status VARCHAR(32) NOT NULL DEFAULT 'pending',
        confirmationprogress DOUBLE PRECISION DEFAULT 0 CHECK (confirmationprogress >= 0 AND confirmationprogress <= 1),
        hash VARCHAR(256) NOT NULL,
        parenthash VARCHAR(256),
        reward DOUBLE PRECISION CHECK (reward >= 0),
        miner VARCHAR(256),
        auxpowdata TEXT,
        submitted BOOLEAN DEFAULT FALSE,
        accepted BOOLEAN DEFAULT FALSE,
        rejectreason VARCHAR(256),
        created TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
        CONSTRAINT aux_blocks_unique_hash UNIQUE (poolid, coin, hash)
    );

    -- Miner stats table
    CREATE TABLE IF NOT EXISTS miner_stats (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        miner VARCHAR(256) NOT NULL,
        worker VARCHAR(256),
        hashrate DOUBLE PRECISION,
        sharespersecond DOUBLE PRECISION,
        created TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );

    -- Pool stats table
    CREATE TABLE IF NOT EXISTS poolstats (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        connectedminers INT,
        poolhashrate DOUBLE PRECISION,
        networkhashrate DOUBLE PRECISION,
        networkdifficulty DOUBLE PRECISION,
        lastnetworkblocktime TIMESTAMP WITH TIME ZONE,
        blockheight BIGINT,
        connectedpeers INT,
        sharespersecond DOUBLE PRECISION,
        created TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );

    -- Payments table
    CREATE TABLE IF NOT EXISTS payments (
        id BIGSERIAL PRIMARY KEY,
        poolid VARCHAR(64) NOT NULL,
        coin VARCHAR(32) NOT NULL,
        address VARCHAR(256) NOT NULL,
        amount DOUBLE PRECISION NOT NULL,
        transactionconfirmationdata TEXT,
        created TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );

    -- Create indexes for performance
    CREATE INDEX IF NOT EXISTS idx_shares_pool_created ON shares(poolid, created DESC);
    CREATE INDEX IF NOT EXISTS idx_shares_miner ON shares(miner, created DESC);
    CREATE INDEX IF NOT EXISTS idx_blocks_pool_created ON blocks(poolid, created DESC);
    CREATE INDEX IF NOT EXISTS idx_blocks_status ON blocks(status);
    CREATE INDEX IF NOT EXISTS idx_blocks_coin ON blocks(coin);
    CREATE INDEX IF NOT EXISTS idx_miner_stats_pool ON miner_stats(poolid, miner, created DESC);
    CREATE INDEX IF NOT EXISTS idx_pool_stats_created ON poolstats(poolid, created DESC);

    -- Indexes for aux_blocks table (merge mining)
    CREATE INDEX IF NOT EXISTS idx_aux_blocks_pool_created ON aux_blocks(poolid, created DESC);
    CREATE INDEX IF NOT EXISTS idx_aux_blocks_coin ON aux_blocks(coin, created DESC);
    CREATE INDEX IF NOT EXISTS idx_aux_blocks_status ON aux_blocks(status);
    CREATE INDEX IF NOT EXISTS idx_aux_blocks_miner ON aux_blocks(miner, created DESC);

    -- Grant permissions (uses GRANT_USER, which defaults to POSTGRES_USER)
    -- Identifier quoted to prevent SQL injection if username contains special chars
    GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "${GRANT_USER}";
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "${GRANT_USER}";

    -- Also grant default privileges so future tables are accessible
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO "${GRANT_USER}";
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO "${GRANT_USER}";
EOSQL
