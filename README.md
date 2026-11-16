# WhatsApp Daily & On-Demand Poll Bot (Go)

A fast, lightweight, and self-hosted WhatsApp bot written in Go that:
1. **Posts a scheduled daily poll** to your team/group at a specific time.
2. **Listens for on-demand chat commands** (like `/tea`, `/coffee`, and custom `/poll ...`) from group members in real time.

- **Engine:** Built with [`whatsmeow`](https://github.com/tulir/whatsmeow) (official multi-device protocol).
- **RAM usage:** ~15 MB.
- **Single Binary:** Pure Go SQLite (no CGO, Node, or Puppeteer required).

---

## Chat Commands Available

Group members can type these commands in any group where the bot is a member:

| Command | Action | Example |
| :--- | :--- | :--- |
| **`/food`** | Creates an instant poll: *"Food today? 🍲"* with **Yes 👍 / No ❌** | `/food` |
| **`/tea`** | Creates an instant poll: *"Tea time! ☕ Who is in?"* with **Yes ☕ / No ❌** | `/tea` |
| **`/coffee`** | Creates an instant poll: *"Coffee break! ☕ Who is joining?"* with **Yes ☕ / No ❌** | `/coffee` |
| **`/poll <Question> \| <Options>`** | Creates an on-the-fly custom poll | `/poll Lunch destination? \| Pizza, Sushi, Burgers` |
| **`/help`** | Displays the available command list | `/help` |

---

## Quick Start

### 1. Build & Run
```bash
./wa-poll-bot
```

### 2. Link Your Phone
On the first run, the bot prints a QR code in your terminal.
1. Open **WhatsApp** on your phone.
2. Go to **Settings** -> **Linked Devices** -> **Link a Device**.
3. Scan the terminal QR code.
4. Credentials are automatically saved to `session.db`.

### 3. Find Your Target Group JID (for the Daily Scheduled Poll)
When the bot connects, it prints all participating groups:
```text
================ PARTICIPATING GROUPS ================
• Team Standup                        -> 120363148920112345@g.us
• Weekend Hikers                      -> 120363299102938102@g.us
======================================================
```

### 4. Configure `.env`
Open `.env` to configure your daily scheduled poll:
```env
TARGET_GROUP_ID=120363148920112345@g.us

# 7:00 AM, Monday to Friday (Saturday & Sunday excluded)
CRON_SCHEDULE=0 7 * * 1-5
TIMEZONE=Asia/Kathmandu

POLL_QUESTION=Food today? 🍲
POLL_OPTIONS=Yes, No
POLL_SELECTABLE_COUNT=1
```
*(Note: Chat commands like `/tea` and `/poll` work even if `TARGET_GROUP_ID` is left empty).*

### 5. Immediate Test Mode
To test sending the scheduled poll immediately without waiting for cron:
```bash
./wa-poll-bot --test-now
```

---

## Running 24/7 on Proxmox LXC Container / Linux Server

### Option A: Direct LXC Service (systemd - Recommended)

Inside your Proxmox Debian/Ubuntu LXC container:

1. **Clone the repository:**
   ```bash
   git clone https://github.com/surajklmn/whatstapp-poll.git /opt/whatsappbot
   cd /opt/whatsappbot
   ```

2. **Prepare configuration:**
   ```bash
   cp .env.example .env
   ```

3. **Build the binary:**
   ```bash
   go build -o wa-poll-bot main.go
   chmod +x wa-poll-bot
   ```

4. **First-time interactive run (Pair WhatsApp):**
   ```bash
   ./wa-poll-bot
   ```
   - A QR code will display directly in your Proxmox container terminal.
   - On your phone: open **WhatsApp** -> **Settings** -> **Linked Devices** -> **Link a Device** and scan the QR code.
   - Once linked, a fresh `session.db` is generated and the terminal will print your groups and their JIDs (`... @g.us`).
   - Press `Ctrl + C` once paired.

5. **Configure `.env` with your group JID:**
   ```bash
   nano .env
   ```
   Paste the target group JID (e.g. `120363...45@g.us`) into `TARGET_GROUP_ID`.

6. **Create `/etc/systemd/system/wa-poll-bot.service`:**
   ```ini
   [Unit]
   Description=WhatsApp Daily & On-Demand Poll Bot
   After=network.target

   [Service]
   Type=simple
   User=root
   WorkingDirectory=/opt/whatsappbot
   ExecStart=/opt/whatsappbot/wa-poll-bot
   Restart=always
   RestartSec=5

   [Install]
   WantedBy=multi-user.target
   ```

7. **Enable and start the service:**
   ```bash
   systemctl daemon-reload
   systemctl enable --now wa-poll-bot
   ```

8. **Monitor live logs:**
   ```bash
   journalctl -u wa-poll-bot -f
   ```

---

### Option B: Docker / Docker Compose

If you run Docker inside your Proxmox LXC container or VM:

1. **Prepare configuration and session files:**
   ```bash
   cp .env.example .env
   touch session.db
   ```

2. **Start the container:**
   ```bash
   docker compose up -d --build
   ```

3. **Check logs / scan QR code (if first run):**
   ```bash
   docker compose logs -f
   ```
