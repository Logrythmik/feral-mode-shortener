# feral-mode-shortener

Link shortener for **feralmo.de**. A short code redirects to its configured
URL (`feralmo.de/buy` → `getferalmode.com/shop`); any unknown path falls
through to `https://getferalmode.com/<path>` so a typo'd or retired link still
lands on the main site. Every hit — including misses — is recorded for
analysis.

Single Go binary, Postgres for storage, embedded admin UI. The schema is
applied idempotently on boot; there is no migration step.

## Routes

| Route | What |
| --- | --- |
| `GET /<code>` | 302 to the link's target. The incoming query string is appended to the target, so UTM params survive. Codes are case-insensitive. |
| `GET /` and unknown paths | 302 to `FALLBACK_BASE_URL` (+ path + query). Misses on well-formed codes are logged too. |
| `GET /admin` | Admin UI (enter the API key once; it's kept in localStorage). |
| `GET /health` | Liveness + DB ping (`/healthz` too, but Google's frontend swallows that path on `*.run.app` URLs). |
| `GET /robots.txt` | Disallows crawling. |

Admin API (all require `Authorization: Bearer <ADMIN_API_KEY>`):

| Route | What |
| --- | --- |
| `GET /api/links` | All links with click counts. |
| `POST /api/links` | `{code?, targetUrl, description?}` — code is generated if omitted. |
| `PATCH /api/links/{code}` | Update `targetUrl` and/or `description`. |
| `DELETE /api/links/{code}` | Delete the link (click history is kept; the code starts falling through). |
| `GET /api/links/{code}/stats` | Total clicks, last-30-days by day, top referrers. |
| `GET /api/misses` | Most-hit unknown codes — catches typo'd campaign links. |

Analytics per click: timestamp, code, referrer, user agent, client IP
(first `X-Forwarded-For` hop). Recorded asynchronously so redirects never
wait on the database.

## Configuration

| Env | Meaning |
| --- | --- |
| `DATABASE_URL` | Postgres URL (required). |
| `ADMIN_API_KEY` | Bearer token for `/api/*` and the admin UI (required). |
| `PORT` | Listen port (default 8080). |
| `FALLBACK_BASE_URL` | Where unknown paths go (default `https://getferalmode.com`). |

## Local development

Uses the studio's dockerized Postgres (`feral-postgres` container), with its
own `shortener` database:

```sh
docker exec feral-postgres psql -U feral -d feral_studio -c 'CREATE DATABASE shortener OWNER feral'
cp .env.example .env.local   # then fill in the local values
set -a; . ./.env.local; set +a; go run .
```

Tests: `go test ./...` (no database needed — handlers are tested against an
in-memory store).

## Production (GCP)

Lives next to feralmode.studio in project `feral-mode-web` (us-central1):
Cloud Run service `feral-shortener` (scale-to-zero, 128Mi, distroless image),
reaching Postgres on the existing `feral-db` VM (`10.128.0.2`) over Direct
VPC egress. It uses its own `shortener` database on that server — same
instance, no coupling to the studio's schema. The VM's nightly `pg_dump`
backup covers only the `feral` database, so add `shortener` to the backup
script on the VM if the link data matters.

First-time setup:

1. Create the database (via the SSH tunnel, see `deploy/provision.sh` header).
2. `bash deploy/provision.sh` — creates secrets `shortener-database-url` and
   `shortener-admin-key` and grants `feral-run` access; add their values as
   it instructs.
3. `bash deploy/deploy.sh` — build, push, deploy.
4. Bind secrets and map the `feralmo.de` domain (commands printed by
   provision.sh). DNS for feralmo.de points at Google's ghs IPs, same as the
   studio domain.

Redeploy after changes: `bash deploy/deploy.sh`.
