Rakazo Manager installs and operates self-hosted Rakazo with Docker Compose.

Use GHCR (`ghcr.io/elie222/rakazo/app:edge`). Do not build the app from source.
Write Compose with literal values, never `${VAR}` interpolation.
Keep bind mounts short: `./pg`, `./data`, `./backups`.
Publish only 127.0.0.1 or 0.0.0.0. --bind-all publishes the web UI; API and Postgres stay on 127.0.0.1. --origin sets BETTER_AUTH_URL/WEB_ORIGIN/API_URL for host Caddy. TLS/Caddy is host-side, not in Compose.
Keep docker-compose.yml small: image, command, env, ports, volumes, healthchecks.
