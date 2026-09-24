# Containers and applications

Preview module for PaNasMs, available through the [official module registry](https://panasms.github.io/module-registry/).

The Go service talks to the local Docker Engine over its Unix socket and uses the
Docker Compose v2 CLI for application projects. Access is administrator-only,
checked by both the core gateway and the module. Docker access is host-level
administrative access, not a sandbox.

## Included in this preview

- Detect the Engine, Compose, versions, daemon availability and conflicting installs.
- Install missing compatible Debian packages from already configured APT sources;
  do not replace existing installations or remove/upgrade existing packages implicitly.
- Create containers from downloaded compatible images, with structured port, environment and folder editors. Compose deployment is hidden while its advanced workflow is under review.
- Show single-container projects as one row; multi-container Compose projects contain inset rows inside a shared group. Standalone containers remain ungrouped.
- Inspect registry platform metadata before pull and local image configuration after pull. Show native OS/architecture compatibility against the Docker host, with unknown status for unavailable metadata or unverified CPU variants; emulation and application resource requirements are outside this check.
- Select Docker Hub tags with paginated loading and an explicit final image reference. Other registries use manual tag entry; digest references remain unchanged.
- Search Docker Hub while typing an image name (400ms debounce, two-character minimum); select a repository or enter a full image reference. Search uses the Docker Engine images/search API.
- Manage container start/stop/restart/remove. Open each container for readable details, live resource metrics, logs and form-based settings.
- Reserve Applications for a future catalog; hide it in this preview. Old `/containers/apps` links redirect to `/containers/containers`.
- List images, networks and named volumes; create/remove networks and volumes.
- Use external bridge networks between applications.
- Persistent server-owned operations, WebSocket updates, task menu and taskbar integration.
- Docker storage settings, visual folder selection and offline data transfer with
  a recovery journal. Original data is retained; bind mounts outside data-root are not moved.
- English, Russian and Ukrainian UI with shared dialogs, themes and controls.

A fresh install requires an empty dedicated directory on a mounted persistent
local Linux data filesystem. The Docker service gets a mount dependency so it
cannot silently write container data to the system disk when that volume is absent.
Existing Docker configurations remain authoritative and are not migrated automatically.
The preview requires Docker Engine >=24 and Compose >=2.20. A separate containerd
snapshotter store and live-restore prevent data migration with an explicit explanation.

Removing this module retains its metadata, Docker and application data. Running
containers do not depend on the module service. Removing an application does not
pass `--volumes`; named volumes and host files remain. Deleting a named volume is
an explicit destructive operation and fails while Docker reports it in use.

## Limits of the first draft

No application catalog or automatic application dependencies. Docker firewall
coexistence with NAS connection sharing still needs separate integration and
acceptance; no sharing group was active during this installation. No image builds,
remote Compose source imports, bundled support files, or adoption of external
Compose projects. Those projects are displayed but their source files are not
rewritten. Container console, private-registry credential UI, desktop app shortcuts
and optional web reverse-proxy configuration remain follow-up work.
An optional web-interface port adds a convenience link; the module does not
assume every published port is an HTTP service. Port conflicts are reported by Docker and leave a failed
operation for inspection. Default image port mappings publish declared ports on
all IPv4 interfaces using the protocols declared by the image; mappings can be removed or edited.

Configuration and history live in `/var/lib/panasms-containers` (root-only).
Resolved Compose environment values are stored in root-only files; do not put them
in public bug reports. Interrupted operations are not silently retried. A failed
Compose deployment may have created some resources; inspect before retrying.

## Development

Dependencies pin published SDK commits; no sibling checkout is required.
Use the current PaNasMs UI (including the container module SDK extensions published on 2026-09-24).
No Docker daemon is needed for unit tests.

```sh
npm ci
npm run build
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/bin/server ./cmd/server
```

Sign the prepared `dist/bin` and `dist/ui` payload using the workspace
`modules/build-archives.py` and a locally trusted signing key. Do not place keys in
this repository. Install the signed archive through the existing module manager.

Interface work follows the [PaNasMs design standard](https://github.com/PaNasMs/panasms/blob/main/docs/ui-design-guidelines.md).

Tagged releases (vX.Y.Z) build ARM64 payloads in GitHub Actions. The registry imports, signs and publishes these payloads; signing keys never enter this repository.
