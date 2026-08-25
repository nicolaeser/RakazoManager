# Rakazo Manager

CLI for installing and operating self-hosted [Rakazo](https://github.com/elie222/rakazo) with Docker Compose.

It writes one `docker-compose.yml`, pulls `ghcr.io/elie222/rakazo/app:edge`, and keeps state in host bind mounts. No app build. No `${VAR}` in Compose. TLS/Caddy stays on the host.

## Requirements

- Linux or macOS on `amd64` or `arm64`
- Docker with Compose v2
- `crontab` only when scheduled backups are used

The GHCR app image is `linux/amd64`. Docker Desktop on Apple Silicon runs it through emulation.

## Install the command

```bash
curl -fsSL https://raw.githubusercontent.com/nicolaeser/RakazoManager/main/install.sh | sh
```

User-only install:

```bash
curl -fsSL https://raw.githubusercontent.com/nicolaeser/RakazoManager/main/install.sh | sh -s -- --user
```

From this tree:

```bash
make build
./build/rakazo-manager version
```

## Install Rakazo

```bash
rakazo-manager install ./stack
rakazo-manager open ./stack
```

The first registered user owns the instance.

Preview without writing files:

```bash
rakazo-manager install --dry-run ./stack
```

## Network and Caddy

Default bind is `127.0.0.1`. `--bind-all` publishes **only the web UI** on `0.0.0.0`. API and Postgres stay on `127.0.0.1`.

Install asks for a public origin (or pass `--origin`). An IP becomes `http://IP:<web-port>`. A domain becomes `https://DOMAIN`. Blank keeps `http://127.0.0.1:<web-port>`.

```bash
rakazo-manager install --origin 10.0.10.3 ./stack
rakazo-manager install --origin rakazo.example.com ./stack
rakazo-manager config set --origin https://rakazo.example.com ./stack
```

Point host Caddy at `127.0.0.1:<web-port>`. The origin is written into `BETTER_AUTH_URL`, `WEB_ORIGIN`, and `API_URL`.

## Common commands

```bash
rakazo-manager start ./stack
rakazo-manager status ./stack
rakazo-manager doctor ./stack
rakazo-manager backup --label manual ./stack
rakazo-manager update ./stack
rakazo-manager rollback ./stack
rakazo-manager self-update --check
```

`config set`, `export-instance`, `import-instance`, `schedule`, `decommission`, completion, and `man` match the operator CLI. Run `rakazo-manager help` for the full list.

## Instance files

```text
./stack/
├── docker-compose.yml              # managed base
├── docker-compose.override.yml     # optional; never overwritten
├── .manager/                       # secrets, metadata, locks
├── pg/                             # Postgres
├── data/                           # bot homes and artifacts
├── workspace/                      # optional host project files
└── backups/
```

The supervisor builds `rakazo/computer:local` from `/app/infra/sandboxes/computer` on first use. That image is not on GHCR.

## Safety

- Web is localhost-only unless `--bind-all` is explicit.
- API and Postgres are always published on `127.0.0.1`.
- Updates and default decommission never delete `./pg`, `./data`, `./workspace`, or `./backups`.
- Secrets, backups, and exports use owner-only permissions.
- Update failure rolls back to the previous image.
