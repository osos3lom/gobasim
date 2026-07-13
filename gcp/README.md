# GCP Production Deployment Guide

This directory contains the necessary configuration files and the cross-compiled binary to deploy the `sawt-gateway` on a Google Cloud Platform (GCP) **e2-micro** instance.

## Deployment Package Files

- [sawt-gateway](file:///c:/Users/Asus/Documents/GitHub/gobasim/gcp/sawt-gateway): The static, cross-compiled Go binary (`linux/amd64`).
- [.env.production](file:///c:/Users/Asus/Documents/GitHub/gobasim/gcp/.env.production): Template for production environment variables.
- [sawt.service](file:///c:/Users/Asus/Documents/GitHub/gobasim/gcp/sawt.service): Systemd service unit to manage the process under least-privilege sandboxing.
- [Caddyfile](file:///c:/Users/Asus/Documents/GitHub/gobasim/gcp/Caddyfile): Reverse proxy configuration that automates TLS certificates and secures request handling.

---

## Step-by-Step Deployment Guide

Follow these steps from your local workstation and the GCP VM to set up a hardened production environment.

### 1. Provision the GCP VM & Firewall

Run these commands from your local machine using the `gcloud` CLI (or set it up in the Google Cloud Console):

#### Create the e2-micro VM Instance
```bash
gcloud compute instances create sawt-gateway \
  --zone=us-west1-b \
  --machine-type=e2-micro \
  --image-family=debian-12 --image-project=debian-cloud \
  --boot-disk-size=30GB --boot-disk-type=pd-standard \
  --tags=sawt-web-server
```

#### Open Firewall Ports for Caddy (HTTP & HTTPS)
```bash
gcloud compute firewall-rules create sawt-allow-web \
  --direction=INGRESS --action=ALLOW --rules=tcp:80,tcp:443 \
  --source-ranges=0.0.0.0/0 --target-tags=sawt-web-server
```

---

### 2. Connect to the VM via SSH

Use IAP tunneling to securely connect without exposing port 22 directly:
```bash
gcloud compute ssh sawt-gateway --zone=us-west1-b --tunnel-through-iap
```

---

### 3. Prepare the Host VM Environment

Execute the following commands **on the remote VM**:

#### Allocate a 1GB Swap File
*Crucial on `e2-micro` (1GB RAM) to prevent memory exhaustion and processes being killed.*
```bash
sudo fallocate -l 1G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
sudo sysctl -w vm.swappiness=10
```

#### Install OS Packages
```bash
sudo apt-get update && sudo apt-get install -y ffmpeg
```

#### Create the Service User & Home Directory
```bash
sudo useradd --system --home /opt/sawt --shell /usr/sbin/nologin sawt
sudo mkdir -p /opt/sawt/voice-spool
sudo chown -R sawt:sawt /opt/sawt
```

---

### 4. Upload Files to the VM

On your **local workstation**, navigate to this `gcp/` directory and upload the build assets:

> [!NOTE]
> Ensure you configure the DNS `A` record for your domain to point to the VM's external IP first. Update the domain name in the `Caddyfile` prior to uploading or editing on the VM.

```powershell
# Upload binary and config files
gcloud compute scp sawt-gateway .env.production sawt.service Caddyfile \
  sawt-gateway:~ --zone=us-west1-b --tunnel-through-iap
```

---

### 5. Install and Harden Files on the VM

Execute the following commands **on the remote VM** to copy the uploaded files to their production locations with appropriate ownership and permissions:

```bash
# Install the binary
sudo install -o sawt -g sawt -m 755 ~/sawt-gateway /opt/sawt/sawt-gateway

# Install the environment configuration
sudo install -o sawt -g sawt -m 600 ~/env.production /opt/sawt/.env

# Clean up local uploads from home directory
rm -f ~/sawt-gateway ~/env.production
```

> [!WARNING]
> Edit `/opt/sawt/.env` directly to input your real production database connection string and API keys before starting:
> `sudo nano /opt/sawt/.env`

---

### 6. Install & Configure Caddy Reverse Proxy

Execute the following commands **on the remote VM**:

#### Install Caddy Server
```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy
```

#### Move Caddyfile to Configuration Directory
```bash
sudo mv ~/Caddyfile /etc/caddy/Caddyfile
sudo chown root:root /etc/caddy/Caddyfile
```

> [!IMPORTANT]
> Edit `/etc/caddy/Caddyfile` to replace `sawt.example.com` with your registered domain.
> `sudo nano /etc/caddy/Caddyfile`

#### Reload Caddy Configuration
```bash
sudo systemctl reload caddy
```

---

### 7. Run and Enable the Sawt Daemon

Execute the following commands **on the remote VM**:

#### Deploy Systemd Service File
```bash
sudo mv ~/sawt.service /etc/systemd/system/sawt.service
sudo chown root:root /etc/systemd/system/sawt.service
```

#### Enable and Start the Daemon
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sawt
```

---

### 8. Verification and Monitoring

Watch the systemd service logs to monitor the startup sequence:
```bash
journalctl -u sawt -f
```

- Confirm the database connects and prints `Schema bootstrap complete.`
- If this is a first-time setup, look in the logs for the **randomly generated admin password** (unless you manually defined `ADMIN_PASSWORD` in your `.env` file).
- The logs will output a **WhatsApp QR code** if the session is not paired. Scan it to authenticate the WhatsApp device.
- Navigate to your domain (e.g. `https://sawt.example.com/login`) to access the dashboard.
