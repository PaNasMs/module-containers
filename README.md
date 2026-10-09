# PaNasMs Containers module

Containers is the Docker management module for
[PaNasMs](https://github.com/PaNasMs/panasms), a browser panel for managing a NAS
on Debian-based Linux. It sets up Docker Engine and Compose from the system's APT
sources, pulls images, creates containers from forms and manages images, networks,
volumes and the Docker data directory. Project website:
<https://panasms.github.io/>.

The module is a preview. The current version is 0.1.17. It requires PaNasMs core
`>=0.2.15,<0.3.0` and module API 1, and is published for ARM64 and AMD64.

## Install

Open **Modules** in the PaNasMs panel and install Containers from the catalog. The
module manager picks the package for your architecture. Signed packages and the
catalog are in the [module registry](https://panasms.github.io/module-registry/).

Only panel administrators can use Containers. Both the core gateway and the module
check this. Docker access is root-level access to the host, not a sandbox.

## What the preview does

- Detects Docker Engine and Compose, their versions, whether the daemon responds
  and conflicting installations. It requires Docker Engine 24 or newer and
  Compose 2.20 or newer.
- Installs missing compatible Debian packages from APT sources that are already
  configured. It does not replace an existing installation and does not remove or
  upgrade existing packages.
- Pulls images. Docker Hub tags load page by page, and other registries take a
  manually entered tag. Digest references are kept as entered. Typing an image
  name searches Docker Hub through the Docker Engine search API after two
  characters and a 400 ms pause.
- Shows whether an image matches the host OS and architecture, using registry
  metadata before the pull and the local image configuration after it. The status
  is "unknown" when metadata is missing or the CPU variant cannot be verified. The
  check does not cover emulation or an application's resource needs.
- Creates containers from downloaded images with forms for published ports,
  environment variables, folders and networks. An optional web interface port adds
  an **Open web interface** link; the module does not assume that every published
  port serves HTTP.
- Starts, stops, restarts and removes containers. Each container has a page with
  details, live resource use, logs and form-based settings.
- Lists images, networks and named volumes, and creates and removes networks and
  volumes. Applications can share external bridge networks.
- Groups the containers of a multi-container Compose project in one row group. A
  single-container project is one row, and standalone containers are not grouped.
- Moves the Docker data directory offline with a recovery journal (see below).
- Runs operations on the server, keeps them across page reloads and reports
  progress through WebSocket updates, Tasks and the top bar.

The UI is available in English, Russian and Ukrainian.

Deploying new Compose projects is hidden while that workflow is under review. The
Applications tab is reserved for a future catalog and is hidden; old
`/containers/apps` links redirect to `/containers/containers`.

## Creating a container

The form checks the name, image, port mappings, NAS port availability and the web
interface port before it starts a background operation (`POST action/check`;
`POST action` repeats the check and answers 409 on a problem). Each problem is shown
next to its field and the form stays open. The operation checks again because the
NAS can change in between. If that second check fails, the module page and Tasks
show the error with a way to reopen the form with the values you entered.

By default each published port uses the container port number on the NAS. If that
port is taken, the form proposes a free one starting from the container port plus
8000 (or from 30000 to 39999 when that would exceed 65535), tries at most eight
candidates and says what it changed. `POST ports/check` flags a taken NAS port
while you edit a mapping. The module tests a port by binding it on the requested
address and closing it right away.

Default mappings publish the ports the image declares, on all IPv4 interfaces and
with the protocols the image declares. You can edit or remove them.

If a container's first start fails, its project data is kept. Creating it again
with the same name reconciles the existing project instead of failing because the
name or its own ports are already in use.

User-correctable server messages are translated through the `server.*` locale
keys, and a Go test fails when a message has no entry.

## Docker storage

A fresh Docker installation needs an empty dedicated folder on a mounted,
persistent, local Linux filesystem. The module adds a systemd mount dependency
(`RequiresMountsFor`) to the Docker service, so Docker cannot write container data
to the system disk when that volume is missing.

For installations it creates, the module selects the classic Docker image store
before the Engine first starts. Docker 29 otherwise defaults to the containerd image
store outside `data-root`, which would leave images and container layers on the
system disk. Existing installations keep their storage driver and are never
switched between stores. Existing Docker configurations stay authoritative and are
not migrated automatically.

The storage settings move the Docker data directory with the Engine stopped and
keep a recovery journal. The original data is kept. Bind mounts outside
`data-root` are not moved. The move is refused, with an explanation, when the
containerd snapshotter store or Docker `live-restore` is enabled.

## Data, removal and recovery

- Configuration and history are stored in `/var/lib/panasms-containers`, readable
  by root only. Resolved Compose environment values are in root-only files there;
  do not attach them to public bug reports.
- Removing the module keeps its metadata, Docker itself and all application data.
  Running containers do not depend on the module service.
- Removing an application does not pass `--volumes`, so named volumes and host
  files remain. Deleting a named volume is a separate destructive action and fails
  while Docker reports the volume in use.
- Interrupted operations are not retried automatically. A failed Compose
  deployment may have created some resources; check them before retrying.

## Not included yet

- An application catalog or automatic application dependencies.
- Image builds, importing remote Compose sources, bundled support files and
  adopting Compose projects created outside the module. Such projects are shown,
  but the module does not rewrite their files.
- A container console, a credential UI for private registries, desktop shortcuts
  and reverse-proxy configuration for web interfaces.
- Tested coexistence of Docker's firewall rules with NAS connection sharing. No
  sharing group was active when this was last installed, so the combination is
  untested.

## Development

The UI is React and TypeScript built with Vite. The server is Go and talks to the
local Docker Engine over its Unix socket and to the Docker Compose v2 CLI for
projects. Dependencies pin published commits of the
[module SDK](https://github.com/PaNasMs/module-sdk), so no sibling checkout is
needed. Interface work follows the
[PaNasMs interface design standard](https://github.com/PaNasMs/panasms/blob/main/docs/ui-design-guidelines.md).

| Path | Contents |
| --- | --- |
| `frontend/` | Containers UI and `locales/` (`en`, `ru`, `uk`) |
| `cmd/server/` | Module service entry point |
| `internal/engine/` | Docker API client, setup, preflight checks, data migration and HTTP handlers |
| `scripts/` | `build.sh`, translation check and payload packaging |

You need Node.js 24, Go 1.26 or newer and Python 3. CI uses Go 1.27.1. The unit
tests do not need a Docker daemon. To build:

```sh
sh scripts/build.sh
```

The script runs `npm ci`, builds the UI, runs `go test ./...` (also available as
`npm test`) and `go vet ./...`, builds `dist/bin/server` with `CGO_ENABLED=0`,
checks that `ru` and `uk` have the same translation keys as `en` and writes
`dist/containers-<version>-<arch>.unsigned.zip`. The architecture comes from
`go env GOARCH`, and packaging fails if the server binary does not match it. Run
`go test -race ./...` as an extra check when you change concurrent code.

The unsigned payload cannot be installed directly. For a local test install, sign
`dist/bin` and `dist/ui` with `modules/build-archives.py` from the main PaNasMs
repository and a locally trusted key, then upload the archive in **Modules**. Never
put signing keys in this repository.

## Release

Update the version in `manifest.json`, `package.json` and `package-lock.json`
together, commit, then push a matching `vX.Y.Z` tag. The
[build workflow](.github/workflows/build.yml) builds both architectures on every
push to `main` and on pull requests. Only a version tag publishes a GitHub release
with the two unsigned payloads, and packaging fails if the tag does not match the
manifest version. The module registry imports, signs and publishes them. Signing
keys never enter this repository.

## License

Original code is licensed under
[PolyForm Noncommercial 1.0.0](LICENSE). See [NOTICE](NOTICE) for third-party
components.
