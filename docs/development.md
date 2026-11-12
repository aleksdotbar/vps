# Development

## Sources

- Tool bootstrap and checks: [Makefile](../panel/Makefile), [scripts](../panel/scripts).
- Dependencies and versions: [Go module](../panel/go.mod), [Go sums](../panel/go.sum), [frontend manifest](../panel/web/package.json), [frontend lock](../panel/web/pnpm-lock.yaml), [Nix lock](../nix/flake.lock).
- Frontend install policy: [pnpm workspace](../panel/web/pnpm-workspace.yaml).
- RPCs: [protobuf](../panel/proto).
- Host inputs: [foundation](../panel/internal/foundation/input.go), [machine](../panel/internal/machine/machine.go).

## Setup and commands

Run from `panel/` with disposable local state and no production credentials:

```sh
make bootstrap static-tools web-bootstrap
make build
```

Select relevant checks from the Makefile; run the repository-wide gate before release:

```sh
make check test check-generated lint vulnerabilities secrets
make release
```

Regenerate contracts/queries only when their sources change:

```sh
make generate
```

Frontend commands are defined in its package manifest. Run from `panel/web/`:

```sh
pnpm install --frozen-lockfile
pnpm typecheck
pnpm lint
pnpm format:check
pnpm build
```

Use current native audit output for dependency decisions; do not preserve passing scan claims or advisory inventories in Markdown.

## Local HTTPS

- Keep the development certificate, private key, SQLite state and master key outside the repository.
- Configure a trusted localhost certificate through `PANEL_DEV_TLS_CERT_FILE` and `PANEL_DEV_TLS_KEY_FILE` using absolute paths.
- Run the backend with disposable state, `-origin https://localhost:5173`, `-listen 127.0.0.1:8080` and the pinned Compose executable.
- Run `pnpm dev` from `panel/web/`. Keep both listeners private; forward the frontend loopback port when using a remote devbox.
- Use [CLI flags](../panel/cmd/panel/main.go) and [Vite configuration](../panel/web/vite.config.ts) as the command contract.

## Resource limits

- One heavy job at a time; inspect available memory first.
- Enclose builds/tests and their child processes in a 6 GiB memory / 200% CPU budget; lower workload when available capacity is smaller.
- Nix: `--max-jobs 1 --cores 1`; VM checks: 35-minute deadline.

Example from `panel/`:

```sh
systemd-run --user --scope --property=MemoryMax=6G --property=CPUQuota=200% \
  timeout 35m env GOMAXPROCS=2 GOFLAGS=-p=2 GOTOOLCHAIN=local make build
```

## Focused integration checks

- Compose/runtime fixtures require the pinned native executables; inspect the affected test's `PANEL_TEST_*` inputs.
- `make test-inspection` exercises native systemd inspection; use it only for changes to that boundary.
- `panel/scripts/test-deployment.sh` exercises deployment and recovery in a disposable Docker/systemd VM; run within the resource budget.
- Select the relevant [Nix check or VM package](../nix/flake.nix); avoid a blanket VM run.
- Keep local fixture evidence separate from real-provider/target acceptance.
