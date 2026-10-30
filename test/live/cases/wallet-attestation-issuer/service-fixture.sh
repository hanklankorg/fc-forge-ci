#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0

# Inert trial settings only. Run inside the configured Java 25 job image.
set -euo pipefail
mkdir -p trial-runtime
keytool -genkeypair -alias trial-issuer -keyalg EC -groupname secp256r1 \
  -dname 'CN=Disposable CI Trial' -validity 1 -storetype PKCS12 \
  -keystore trial-runtime/issuer.p12 -storepass trial-only -keypass trial-only -noprompt
cat > trial-runtime/runtime.env <<'SETTINGS'
WUA_KEYSTORE_PATH=file:/trial/issuer.p12
WUA_KEYSTORE_PASSWORD=trial-only
WUA_KEYSTORE_ALIAS=trial-issuer
WALLET_ATTESTATION_ISSUER_ISSUER=Disposable CI Trial
WALLET_ATTESTATION_ISSUER_WIA_STATUS={"status_list":{"idx":1,"uri":"https://example.invalid/status/1"}}
WALLET_ATTESTATION_ISSUER_WIA_STATUS_MAINTENANCE_DAYS=365
WALLET_ATTESTATION_ISSUER_WIA_VALIDITY_MINUTES=60
WALLET_ATTESTATION_ISSUER_WIA_CLIENT_ID=ci-trial
WALLET_ATTESTATION_ISSUER_WIA_WALLET_NAME=CI Trial
WALLET_ATTESTATION_ISSUER_WIA_WALLET_VERSION=0.0.0
WALLET_ATTESTATION_ISSUER_WIA_WALLET_LINK=https://example.invalid/wallet
WALLET_ATTESTATION_ISSUER_WIA_CERTIFICATION_INFORMATION=UNCERTIFIED
SETTINGS
