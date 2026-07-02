# Go Whatsmeow Gateway Migration Walkthrough

We have successfully dropped the TypeScript Baileys gateway and replaced it with a lightweight, high-performance Go-based WhatsApp gateway using the `whatsmeow` library.

---

## Changes Made

### 1. Database Schema Cleanup
- **[schema.ts](../packages/database/src/schema.ts)**: Removed Baileys-specific auth tables (`wa_creds` and `wa_signal_keys`). Whatsmeow automatically manages its own session storage inside Neon Postgres using `whatsmeow_` prefixed tables.

### 2. Workspace & Run Script Updates
- **[package.json](../package.json)**: Updated the `"dev:gateway"` script to point to the Go entrypoint:
  ```json
  "dev:gateway": "go run apps/gateway-whatsmeow/main.go"
  ```

### 3. Deletion of Baileys Gateway
- Completely removed the `apps/gateway-baileys` directory containing all TypeScript Baileys socket handlers, auth stores, and scripts.

### 4. Implementation of Go Gateway (`apps/gateway-whatsmeow`)
- **[go.mod](../apps/gateway-whatsmeow/go.mod)**: Initialized Go module.
- **[main.go](../apps/gateway-whatsmeow/main.go)**: Implemented:
  - Whatsmeow client connection logic.
  - Interactive QR terminal printing and Pairing Code generation depending on `PAIR_PHONE_NUMBER` environment variable.
  - Media downloads for inbound voice notes (decoding and forwarding base64 OGG/Opus audio).
  - Signature signing (`x-swa-signature` and `x-swa-timestamp` HMAC headers) for webhook integrity.
  - POST `/send` HTTP endpoint with signature verification to dispatch text and base64 audio messages.
  - GET `/health` HTTP endpoint to report socket state and uptime.
- **[README.md](../apps/gateway-whatsmeow/README.md)**: Documented configuration and usage instructions for Go developers.
- **[setup-vm.sh](../apps/gateway-whatsmeow/deploy/setup-vm.sh)**: Stage 1 script to bootstrap Go, clone the repo, and compile the gateway statically.
- **[install-service.sh](../apps/gateway-whatsmeow/deploy/install-service.sh)**: Stage 3 script to configure and start the `sawt-gateway` systemd service.
- **[sawt-gateway.service](../apps/gateway-whatsmeow/deploy/sawt-gateway.service)**: Static systemd unit file template.

### 5. Documentation Updates
- **[BLUEPRINT.md](BLUEPRINT.md)**: Updated target architecture diagram, gap table, and roadmap descriptions.
- **[SKILL.md](../.claude/skills/codebase-map/SKILL.md)**: Updated workspace files mapping.
- **[GCP-GATEWAY-SETUP.md](GCP-GATEWAY-SETUP.md)**: Updated step-by-step VM setup instructions for Go.

---

## Verification

- Verified that all Node/TypeScript workspaces compile and build successfully without old Baileys database structures.
- Tested the Go gateway module structure and scripts (GCP deployment ready).
