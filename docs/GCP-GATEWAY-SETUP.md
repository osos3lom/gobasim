# GCP setup — WhatsApp Gateway (Go whatsmeow) on a free e2-micro VM

Deploys `apps/gateway-whatsmeow` to a GCE **e2-micro** instance (GCP's always-free tier, forever, in eligible US regions) so it can hold a real, always-on WhatsApp connection instead of your laptop. This guide gets you to a **private, working test**: a real WhatsApp number paired to the VM, inbound messages visible in the gateway's logs. No public exposure, no backend wiring yet — those are explicit next steps at the bottom.

**Target:** deployment decisions locked for this pass — GCE e2-micro VM, systemd-managed, US free-tier region. See [BLUEPRINT.md](BLUEPRINT.md) §2/§18 [A3] for the always-on rationale.

---

## 0. Prerequisites

- An existing GCP project with billing enabled, and the `gcloud` CLI installed + authenticated locally (`gcloud auth login`, `gcloud config set project YOUR_PROJECT_ID`).
- A Neon `DATABASE_URL` — the **same** one the dashboard/backend use (auth state is stored there inside whatsmeow SQL tables).
- A phone with WhatsApp, willing to be linked as a test number (Linked Devices — this does **not** require a second SIM; any existing WhatsApp account can link an additional device).
- Repo access from the VM: if `github.com/osos3lom/stt-tts` is public, plain `git clone` works from the VM as-is. If it's private, either set up a fine-grained GitHub PAT and clone with `https://<token>@github.com/...`, or generate an SSH deploy key on the VM and add it to the repo's Deploy Keys.

**Free-tier facts that matter:** the always-free e2-micro allowance is **one instance per billing account** (not per project), only in `us-west1`, `us-central1`, or `us-east1`, with up to 30 GB-months of standard persistent disk. If you already run another free e2-micro anywhere on this billing account, this one will incur cost.

---

## 1. Create the VM

Pick one free-tier region/zone (this guide uses `us-west1-b`):

```bash
gcloud compute instances create sawt-gateway \
  --zone=us-west1-b \
  --machine-type=e2-micro \
  --image-family=debian-12 \
  --image-project=debian-cloud \
  --boot-disk-size=30GB \
  --boot-disk-type=pd-standard \
  --tags=sawt-gateway
```

No firewall rules are opened. The VM needs no inbound public access for this test — whatsmeow's connection to WhatsApp is **outbound**, and `/health`/`/send` are only checked locally over SSH for now. `gcloud compute ssh` uses IAP tunneling by default, so you don't need to open port 22 to the internet either.

---

## 2. SSH in and run stage 1 (bootstrap)

```bash
gcloud compute ssh sawt-gateway --zone=us-west1-b
```

On the VM:

```bash
curl -fsSL https://raw.githubusercontent.com/osos3lom/stt-tts/main/apps/gateway-whatsmeow/deploy/setup-vm.sh -o setup-vm.sh
bash setup-vm.sh
```

(If the repo is private, `git clone` inside the script will prompt/fail — clone manually first with your token/deploy key, then re-run: `REPO_DIR=~/stt-tts bash setup-vm.sh`.)

This installs Go, clones the repo, and compiles the Go static binary. Because Go binaries are compiled and lightweight, we do not need Node.js or swap files to build/run the gateway.

---

## 3. Fill in secrets and pair (stage 2, manual — do not automate secret entry)

```bash
cd ~/stt-tts/apps/gateway-whatsmeow
nano .env   # or env.local
```

Fill in:
- `DATABASE_URL` — your Neon connection string (same DB the rest of the platform uses).
- `GATEWAY_SHARED_SECRET` — generate with: `openssl rand -hex 32`.
- `WEBHOOK_URL` — Leave this **unset** during testing, or point to your backend webhook if configured.

Then pair:

```bash
cd ~/stt-tts/apps/gateway-whatsmeow
# Pairing Code Flow (Recommended over SSH)
PAIR_PHONE_NUMBER=9665XXXXXXXXX go run main.go

# QR Code Flow (Console ASCII QR)
go run main.go
```

If using `PAIR_PHONE_NUMBER`, enter the code shown under **Linked Devices ▸ Link with phone number instead** on WhatsApp.

You should see `WhatsApp connection established.` Once logged in, you can stop the process (Ctrl+C) — pairing is one-time; the saved session in Neon Postgres is what the always-on gateway process reconnects with from now on.

---

## 4. Install stage 3 (systemd service)

```bash
cd ~/stt-tts/apps/gateway-whatsmeow
bash deploy/install-service.sh
```

Generates and enables a `sawt-gateway` systemd unit (auto-restart on crash, starts on boot). Check it:

```bash
sudo systemctl status sawt-gateway
curl -s localhost:8080/health
# {"ok": true, "status": "open", "connected": true, ...}
```

---

## 5. Test it — send a real WhatsApp message

From another phone, message the number you paired. Watch the gateway log it:

```bash
journalctl -u sawt-gateway -f
```

Expect the message's `from`/`pushName`/`text` to be outputted to the logs, confirming that a real WhatsApp message reached the VM, was decrypted (Signal protocol via whatsmeow), and was processed.

---

## 6. Next steps (not done in this pass)

- **Wire up the backend.** Once `apps/backend` is deployed somewhere reachable from this VM, set `WEBHOOK_URL` in `.env` to its `/webhook/inbound` URL, restart (`sudo systemctl restart sawt-gateway`), and inbound messages start flowing into the real LangGraph pipeline.
- **Expose it securely if the backend needs to reach the gateway too** (`/send` replies) — prefer a **Cloudflare Tunnel** (HTTPS, no open inbound port) over opening a GCP firewall rule.
- **Redeploys:** `cd ~/stt-tts && git pull && cd apps/gateway-whatsmeow && go build -o gateway main.go && sudo systemctl restart sawt-gateway`.
- **Health monitoring:** `/health` is public-plane-ready — point an uptime check or the dashboard's fallback health route (`apps/dashboard/src/app/api/health/route.ts`) at it.
