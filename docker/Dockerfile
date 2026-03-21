# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                                                                            ║
# ║                       SPIRAL POOL - DOCKER IMAGE                           ║
# ║                                                                            ║
# ║              Multi-Coin Solo Mining Pool (Single-Coin Mode)                ║
# ║                                                                            ║
# ║   Spiral Pool Contributors                                                 ║
# ║                                                                            ║
# ║   Base: Ubuntu 24.04 LTS                                                   ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

FROM ubuntu:24.04

LABEL maintainer="Spiral Miner"
LABEL description="Spiral Pool - Multi-Coin Solo Mining Pool"
LABEL version="1.1.0"

# Prevent interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=UTC

# Build arguments for configuration
ARG GO_VERSION=1.26.1
ARG DIGIBYTE_VERSION=8.26.2
ARG POSTGRES_VERSION=18

# Environment variables for runtime
ENV INSTALL_DIR=/spiralpool
ENV POOL_USER=spiraluser
ENV PATH="${PATH}:/usr/local/go/bin:${INSTALL_DIR}/bin"

# ═══════════════════════════════════════════════════════════════════════════════
# SYSTEM DEPENDENCIES
# ═══════════════════════════════════════════════════════════════════════════════

RUN apt-get update && apt-get install -y --no-install-recommends \
    # Build essentials
    build-essential \
    pkg-config \
    # Network tools
    curl \
    wget \
    ca-certificates \
    openssl \
    gnupg \
    lsb-release \
    # ZMQ for block notifications
    libzmq3-dev \
    # PostgreSQL client
    postgresql-client \
    # Python for dashboard and sentinel
    python3 \
    python3-pip \
    python3-venv \
    # Process management
    supervisor \
    # Utilities
    jq \
    netcat-openbsd \
    iputils-ping \
    dnsutils \
    file \
    vim \
    htop \
    # HA/VIP networking (macvlan, arping for gratuitous ARP)
    iproute2 \
    iputils-arping \
    # envsubst for config templating
    gettext-base \
    && rm -rf /var/lib/apt/lists/*

# ═══════════════════════════════════════════════════════════════════════════════
# GO INSTALLATION
# WARNING: ARM64 (aarch64) support is EXPERIMENTAL and UNTESTED.
# Go cross-compilation and stratum binary compatibility are not verified on ARM.
# ═══════════════════════════════════════════════════════════════════════════════

RUN ARCH=$(dpkg --print-architecture) \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tar.gz \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && go version

# ═══════════════════════════════════════════════════════════════════════════════
# CREATE POOL USER AND DIRECTORIES
# ═══════════════════════════════════════════════════════════════════════════════

RUN useradd -r -m -s /bin/bash ${POOL_USER} \
    && mkdir -p ${INSTALL_DIR}/{bin,config,logs,data} \
    && mkdir -p /home/${POOL_USER}/.spiralsentinel \
    && mkdir -p /home/${POOL_USER}/.spiralpool \
    && chown -R ${POOL_USER}:${POOL_USER} ${INSTALL_DIR} \
    && chown -R ${POOL_USER}:${POOL_USER} /home/${POOL_USER}

# ═══════════════════════════════════════════════════════════════════════════════
# COPY APPLICATION FILES
# ═══════════════════════════════════════════════════════════════════════════════

# Copy Go module files first for cache-friendly dependency download.
# go.mod/go.sum change only when dependencies change, so this layer is cached
# across source code edits — avoiding expensive re-downloads on every build.
COPY src/stratum/go.mod src/stratum/go.sum ${INSTALL_DIR}/src/stratum/

WORKDIR ${INSTALL_DIR}/src/stratum

# Download dependencies (cached until go.mod/go.sum change)
RUN go mod download

# Now copy the full stratum source code
COPY --chown=${POOL_USER}:${POOL_USER} src/stratum ${INSTALL_DIR}/src/stratum

# Copy dashboard
COPY --chown=${POOL_USER}:${POOL_USER} src/dashboard ${INSTALL_DIR}/src/dashboard

# Copy sentinel
COPY --chown=${POOL_USER}:${POOL_USER} src/sentinel ${INSTALL_DIR}/src/sentinel

# Copy configuration templates
COPY --chown=${POOL_USER}:${POOL_USER} docker/config ${INSTALL_DIR}/config

# Remove config.yaml if it exists (will be bind-mounted at runtime)
# This prevents issues if the host had config.yaml as a directory during build
RUN rm -rf ${INSTALL_DIR}/config/config.yaml || true

# ═══════════════════════════════════════════════════════════════════════════════
# BUILD SPIRAL STRATUM
# ═══════════════════════════════════════════════════════════════════════════════

# Build with ZMQ support
RUN CGO_ENABLED=1 go build -o ${INSTALL_DIR}/bin/spiralstratum ./cmd/spiralpool

# Build spiralctl (pool administration CLI, includes VIP management)
RUN go build -o ${INSTALL_DIR}/bin/spiralctl ./cmd/spiralctl

# ═══════════════════════════════════════════════════════════════════════════════
# SETUP PYTHON ENVIRONMENT FOR DASHBOARD/SENTINEL
# ═══════════════════════════════════════════════════════════════════════════════

COPY src/dashboard/requirements.txt /tmp/requirements.txt
RUN python3 -m venv ${INSTALL_DIR}/venv \
    && ${INSTALL_DIR}/venv/bin/pip install --no-cache-dir -r /tmp/requirements.txt \
    && rm /tmp/requirements.txt \
    && chown -R ${POOL_USER}:${POOL_USER} ${INSTALL_DIR}/venv

# Copy Python scripts to bin
RUN cp ${INSTALL_DIR}/src/dashboard/dashboard.py ${INSTALL_DIR}/bin/ \
    && cp ${INSTALL_DIR}/src/sentinel/SpiralSentinel.py ${INSTALL_DIR}/bin/ \
    && chmod +x ${INSTALL_DIR}/bin/*.py

# ═══════════════════════════════════════════════════════════════════════════════
# SUPERVISOR CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════

COPY docker/supervisord.conf /etc/supervisor/conf.d/spiralpool.conf

# ═══════════════════════════════════════════════════════════════════════════════
# ENTRYPOINT SCRIPT
# ═══════════════════════════════════════════════════════════════════════════════

COPY docker/scripts/stratum-entrypoint.sh /usr/local/bin/stratum-entrypoint.sh
RUN chmod +x /usr/local/bin/stratum-entrypoint.sh

# ═══════════════════════════════════════════════════════════════════════════════
# HEALTHCHECK
# ═══════════════════════════════════════════════════════════════════════════════

HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD curl -f http://localhost:4000/api/pools || exit 1

# ═══════════════════════════════════════════════════════════════════════════════
# EXPOSE PORTS
# ═══════════════════════════════════════════════════════════════════════════════

# Stratum V1 (miners connect here — actual port set by POOL_COIN)
EXPOSE 3333

# REST API
EXPOSE 4000

# Prometheus metrics
EXPOSE 9100

# ═══════════════════════════════════════════════════════════════════════════════
# VOLUMES
# ═══════════════════════════════════════════════════════════════════════════════

# Persist logs and shared data (managed by docker-compose named volumes).
# NOTE: /spiralpool/config is NOT declared as a VOLUME because compose uses a
# file-level bind mount for the config template. A VOLUME declaration here would
# create orphan anonymous volumes on every container recreation.
VOLUME ["/spiralpool/logs", "/spiralpool/data"]

# ═══════════════════════════════════════════════════════════════════════════════
# ENTRYPOINT
# ═══════════════════════════════════════════════════════════════════════════════

WORKDIR ${INSTALL_DIR}

# Run as spiraluser - the designated pool admin user
# This is more secure than running as root and dropping privileges
USER ${POOL_USER}

# Use entrypoint script to generate config from template, then start supervisor
CMD ["/usr/local/bin/stratum-entrypoint.sh"]
