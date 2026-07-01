<p align="center">
<img src="/assets/logo.png" alt="ExpenseOwl Logo" width="200" height="200" /><br>
</p>

<h1 align="center">ExpenseOwl</h1><br>

<p align="center">
<a href="https://github.com/tanq16/expenseowl/actions/workflows/release.yml"><img src="https://github.com/tanq16/expenseowl/actions/workflows/release.yml/badge.svg" alt="Release"></a>&nbsp;<a href="https://github.com/Tanq16/expenseowl/releases"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/tanq16/expenseowl"></a>&nbsp;<a href="https://hub.docker.com/r/tanq16/expenseowl"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/tanq16/expenseowl"></a>
</p>

<p align="center">
<a href="#why-create-this">Why Create This?</a>&nbsp;&bull;&nbsp;<a href="#features">Features</a>&nbsp;&bull;&nbsp;<a href="#screenshots">Screenshots</a><br><a href="#installation">Installation</a>&nbsp;&bull;&nbsp;<a href="#usage">Usage</a>&nbsp;&bull;&nbsp;<a href="#contributing">Contributing</a>
</p>

<br>

<p align="center">
<b>ExpenseOwl</b> is an extremely simple self-hosted expense tracking system with a modern monthly pie-chart visualization and cashflow showcase.
</p>

<br>

# Why Create This?

There are a ton of amazing projects for expense tracking across GitHub ([Actual](https://github.com/actualbudget/actual), [Firefly III](https://github.com/firefly-iii/firefly-iii), etc.). They're all incredible! I just don't find them *fast* and *simple*. They offer too many features I never use (like accounts or complex budgeting). *Don't get me wrong!* They're amazing when complexity is needed, but I wanted something ***dead simple*** that gives me a quick monthly look at my expenses. NOTHING else!

So, I created this project and I use it in my home lab for expenses. The primary intention is to track spending across your categories in a simplistic manner. No complications, searching, budgeting. This is *not* a budgeting app; it's for tracking.

# Features

### Core Functionality

- Quick expense add (only date, amount, and category are required)
- Multi-user: each user logs in and sees only their own expenses; an admin role sees everyone's and manages users
- Recurring expenses
- Custom categories, currency symbols, and start date via app settings
- Optional tags for further classification
- Beautiful interface with both light and dark themes
- Self-contained binary and container image to ensure no internet interaction
- Multi-architecture Docker container with support for persistent storage
- PWA support for using the app on smartphone

### Visualization

1. Main dashboard - category breakdown (pie chart) and cashflow indicator
    - Click on a category to exclude it from the pie chart; click again to add it back
    - Visualize the month's breakdown without considering some categories like Rent
    - Shows the month's total spending
2. Table view for detailed expense listing
    - View monthly or all expenses chronologically and delete them (hold shift to skip confirm)
    - Use the browser to search for a name or tags if needed
    - Tags show up if at least one transaction uses it; 
3. Settings page for configurations and additional features
    - Reorder, add, or remove custom categories
    - Select a custom currency symbol and a custom start date
    - Exporting data as CSV and import CSV from virtually anywhere

### Progressive Web App (PWA)

The front end of ExpenseOwl can be installed as a Progressive Web App on desktop and mobile devices (i.e., the back end still needs to be self-hosted). To install:

- Desktop: Click the install icon in your browser's address bar
- iOS: Use Safari's "Add to Home Screen" option in the share menu
- Android: Use Chrome's "Install" option in the menu

# Screenshots

Dashboard Showcase:

| | Desktop View | Mobile View |
| --- | --- | --- |
| Dark | <img src="/assets/ddark-main.png" alt="Dashboard Dark" /> | <img src="/assets/mdark-main.png" alt="Mobile Dashboard Dark" /> |
| Light | <img src="/assets/dlight-main.png" alt="Dashboard Light" /> | <img src="/assets/mlight-main.png" alt="Mobile Dashboard Light" /> |

<details>
<summary>Expand this to see screenshots of other pages</summary>

| | Desktop View | Mobile View |
| --- | --- | --- |
| Table Dark | <img src="/assets/ddark-table.png" alt="Dashboard Dark" /> | <img src="/assets/mdark-table.png" alt="Mobile Dashboard Dark" /> |
| Table Light | <img src="/assets/dlight-table.png" alt="Dashboard Light" /> | <img src="/assets/mlight-table.png" alt="Mobile Dashboard Light" /> |
| Settings Dark | <img src="/assets/ddark-settings.png" alt="Table Dark" /> | <img src="/assets/mdark-settings.png" alt="Mobile Table Dark" /> |
| Settings Light | <img src="/assets/dlight-settings.png" alt="Table Light" /> | <img src="/assets/mlight-settings.png" alt="Mobile Table Light" /> |

</details>

# Installation

The recommended installation method is Docker. To run the container via CLI, use the following command:

```bash
docker run --rm -d \
  --name expenseowl \
  -p 8080:8080 \
  -v expenseowl:/app/data \
  tanq16/expenseowl:main
```

To use Docker compose, use this YAML definition:

```yaml
services:
  expenseowl:
    image: tanq16/expenseowl:main
    restart: unless-stopped
    ports:
      - 5006:8080 # change 5006 to what you want to expose on
    volumes:
      - /home/tanq/expenseowl:/app/data # change dir as needed
```

<details>
<summary>Expand this to see additional execution options</summary>

### Using the Binary or Building from Source

Download the appropriate binary from the project releases. The binary automatically sets up a `data` directory in your CWD, and starts the app at `http://localhost:8080`.

To build the binary yourself:

```bash
git clone https://github.com/tanq16/expenseowl.git && \
cd expenseowl && \
go build ./cmd/expenseowl
```

### Kubernetes Deployment

This is a community-contributed Kubernetes spec. Treat it as a sample and review before deploying to your cluster. Read the [associated readme](./kubernetes/README.md) for more information.

</details>

# Usage

Once deployed, use the web interface to do everything. Access it through your browser:

- Dashboard: `http://localhost:8080/`
- Table View: `http://localhost:8080/table`
- Settings: `http://localhost:8080/settings`

> [!NOTE]
> This app does not include authentication, so deploy carefully. I don't want to add half-baked authentication, so use Authelia, or equivalent as needed. ExpenseOwl works well with a reverse proxy like Nginx Proxy Manager too and is intended for homelab use only.

### Conventions

Since writing the app, I've found a ton of ways applications handle expenses. Release v4.0 solidifies the conventions I will continue to maintain the app in.

- Expenses are stored as -ve values
- Expense dates are stored as UTC strings in RFC3339 format, however, the frontend hides the time value from the user; users are meant to select a date, and the current local time is automatically added to the given date
- Future and recurring expenses extending into future dates are added immediately to the backend
- The primary way to use ExpenseOwl is to quick review the month's stats via the pie chart - this allows users to make a mental note and soft decision of where to spend money, without the effort of maintaining a budget
- Categories are meant to be used as a classification criteria - example, how much did I spend on food, groceries, and utilities, etc.
- Tags are optional and are meant to assign features and characteristics to expenses.

> [!NOTE]
> While these conventions can change during the project's lifecycle, largely, the intention (stemming from the motivation to build ExpenseOwl) behind simple, manual, easy tracking will not change.

### Configuration Options

With the exception of [Data backends](#data-backends), all configuration of ExpenseOwl happens via the application UI. The list of all such options available via the settings page (`/settings` endpoint) is as follows:

- Category Settings:
- Currency Symbol:
  - This is a frontend symbol configuration on what symbol to use to show amount values
  - Each currency has its default behavior for using `,` or `.` as separators (and if it uses decimals or not)
- Start Date:
  - This is a custom day of the month from when the expenses will be displayed
  - Example: setting it to 5 means, expenses for each month will be counted from 5th to next month's 4th
- Recurring Transactions:
  - Given a value for number of occurences and a start date, the app will add the expenses accordingly
  - Recurring transactions will be listed at the bottom of the page and can be edited/removed (all or future only transactions)
  - Recurring transactions allow similar options as normal expenses - category, tags, amount, name
- Theme Settings: supports light and dark theme, with default behavior to adapt to system
- Import/Export Data: covered under [Data Import/Export](#data-importexport)
- User Management (admin only): create users, reset passwords, and delete users from the settings page

### Authentication

ExpenseOwl requires a login. Each user sees and edits only their own expenses; an
**admin** sees everyone's expenses and manages users (category/card/currency/start-date
settings are shared and admin-only to edit).

On first run, if no users exist, an initial admin is created from these environment
variables (both optional):

| Variable | Sample Value | Details |
| --- | --- | --- |
| ADMIN_USERNAME | admin | username for the bootstrap admin (defaults to `admin`) |
| ADMIN_PASSWORD | a-strong-secret | password for the bootstrap admin (defaults to `admin` — **change it immediately**) |

Sessions are kept in memory (cookie-based), so a restart requires logging in again.

### Telegram Bot (AI expense capture)

ExpenseOwl can run an optional Telegram bot that reads an expense from a text message
**or a photo of a receipt** and loads it for the right user. It uses Claude (Opus 4.8,
with vision for photos) to extract the name, amount, category, card, date, and tags. The
bot then shows an **interactive confirmation** with inline buttons — pick the card and
category, edit the concept or date, and confirm — before saving through the same path as
the web UI. When you send a photo, the **receipt image is kept as backup** and is viewable
from the table view (a receipt icon on each expense that has one; owner or admin only).
After saving, the confirmation message keeps **🗑️ Delete** and **✏️ Edit** buttons so the
user can remove or re-edit that expense straight from Telegram.

The bot is enabled only when `TELEGRAM_BOT_TOKEN`, `ANTHROPIC_API_KEY`, **and**
`TELEGRAM_WEBHOOK_SECRET` are all set; otherwise the app logs `Telegram bot disabled` and
runs normally. The secret is required because the webhook route is public — without it,
anyone could POST forged expense updates.

| Variable | Sample Value | Details |
| --- | --- | --- |
| TELEGRAM_BOT_TOKEN | 123456:ABC-... | bot token from [@BotFather](https://t.me/BotFather) |
| ANTHROPIC_API_KEY | sk-ant-... | required for the AI extraction |
| TELEGRAM_WEBHOOK_SECRET | a-random-string | **required**; sent as the `X-Telegram-Bot-Api-Secret-Token` header so only Telegram can post to the webhook |

Setup:

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy its token into `TELEGRAM_BOT_TOKEN`.
2. Set `ANTHROPIC_API_KEY` and a random `TELEGRAM_WEBHOOK_SECRET` (e.g. `openssl rand -hex 16`).
3. Deploy. On startup the app registers the webhook automatically at
   `https://<RAILWAY_PUBLIC_DOMAIN>/telegram/webhook` (if `RAILWAY_PUBLIC_DOMAIN` is set;
   otherwise register it manually).
4. Each person sends a message to the bot. If they aren't linked yet, the bot replies with
   their **chat ID**. An admin pastes that ID into **Settings → Usuarios → Telegram** for the
   matching user (you can also get it from [@userinfobot](https://t.me/userinfobot)).
5. From then on, the user can send `Almuerzo 4500 en restaurante` or a receipt photo and the
   expense is recorded under their account.

Expenses created by the bot are attributed to the linked user, so per-user isolation and the
admin's consolidated view work the same as in the web app.

### Data Backends

ExpenseOwl supports two data backends - JSON (default), and Postgres. Postgres was added with v4.0 of the app primarily for homelabbers to reuse their Postgres instances as needed for better backup compatibility.

Ideally, you need not configure anything differently for the JSON backend. ExpenseOwl automatically creates the data directory and the `.json` files. You may, however, want to mount a specific volume to `/app/data` within the container for persistence.

For configuring Postgres, use the following environment variables:

| Variable | Sample Value | Details |
| --- | --- | --- |
| STORAGE_TYPE | postgres | defaults to `json`, hence JSON backend is default |
| STORAGE_URL | "localhost:5432/expenseowldb" | format - SERVER/DB - the sslmode value is set by the next variable |
| STORAGE_SSL | require | can be one of `disable` (default), `verify-full`, `verify-ca`, or `require` |
| STORAGE_USER | testuser | the user to authenticate with your Postgres instance |
| STORAGE_PASS | testpassword | the password for the Postgres user |

The app has been tested with SSL mode for Postgres set to disable for simplicity.

> [!TIP]
> The environment variables can be set for using `-e` in the command line or `environment` in a compose stack.

> [!TIP]
> Having learnt more Go, I introduced the Storage interface in v4.0, making it easy to add any storage backend by simply implementing the interface.

### Data Import/Export

ExpenseOwl is meant to make things simple, and importing CSV abides by the same philosophy. ExpenseOwl will accept any CSV file as long as it contains the columns - `name`, `category`, `amount`, and `date`. This is case-insensitive so `name` or `Name` doesn't matter.

> [!TIP]
> This feature allows ExpenseOwl to use exported data from any tool as long as the required categories are present, making it insanely easy to shift from any provider.

> [!WARNING]
> The recommended format for the date is RFC3339. Additionally, ExpenseOwl can ingest several other time formats, including a short, human written date like `2012/8/14` (14th August 2012).
> HOWEVER !!!
> ExpenseOwl only ingests date in YYYY-MM-DD (this order). ExpenseOwl does NOT deal with MM/DD or DD/MM. Full 4 digit year comes first, followed by month, and lastly the date.

> [!NOTE]
> ExpenseOwl goes through every row in the imported data, and will intelligently fail on rows that have invalid or absent data. There is a 10 millisecond delay per record to reduce disk/db overhead, so please allow appropriate time for ingestion (eg. ~10 seconds for 1000 records).

Data exported as CSV will include expense IDs, so when importing the same CSV file, IDs will be maintained and skipped appropriately.

An `Import from ExpenseOwl v3.2-` will be present for v4.X to allow pulling in data from past releases.

# Contributing

Contributions are welcome; please ensure they align with the project's philosophy of maintaining simplicity by strictly using the current tech stack (Go for backend; HTML, CSS, JS for frontend). It is intended for home lab use, i.e., a self-hosted first approach (containerized use). Consider the following:

- Additions should have sensible defaults without breaking foundations
- Environment variables can be used for system configuration in container and binary
- Found a typo or need to ask a question? Please open an issue instead of a PR
- To add a new backend type (say SQL, NocoDB, etc.), a new file can be added in the backend that implements the Storage interface
