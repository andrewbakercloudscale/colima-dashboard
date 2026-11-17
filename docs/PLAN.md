# Colima UI: version 1 plan

A small, local, web-based front end for [Colima](https://github.com/abiosoft/colima), giving a MacBook
the day-to-day screens people used Rancher Desktop for, without an Electron app running in the
background. One Go binary, opened in the browser, bound to this Mac only.

Status: planned, no code yet (17 Nov 2026).

## Goals

- **Lightweight.** One binary of about 15 MB, no runtime to install, idle memory well under 50 MB.
  Compare with Rancher Desktop's Electron app for the blog post (measure both, see "Blog numbers").
- **Familiar.** The Rancher Desktop features people actually use, laid out the same way: general
  status, containers, images, volumes, Kubernetes, preferences, troubleshooting.
- **Safe.** Colima's Docker socket is equivalent to root in the VM, so the UI is local only and
  protected from other web pages (see "Security"). Never listen on anything but 127.0.0.1.
- **Honest.** Shows what Colima and Docker report; when a call fails, says so and why.

Not goals for version 1: Windows or Linux, a menu bar app, extensions, multi-user, remote access.

## Port and launch

- **Default address: `http://127.0.0.1:7766`.** Checked free on this Mac on 17 Nov 2026. Avoids 5000 and
  7000 (macOS AirPlay), 3000, 8000, 8080, 9000 (Portainer), 6463 to 6472 (Discord) and 8740 (the
  AWS Cloud Risk Review console).
- `--port` changes it. If the port is taken, exit with a clear message; do not pick another silently.
- `colima-ui` starts the server and opens the browser with a one-time token in the URL;
  `--no-browser` for launchd.
- `colima-ui install-service` / `uninstall-service`: a LaunchAgent so it starts at login
  (`~/Library/LaunchAgents/io.github.colima-ui.plist`, RunAtLoad, no KeepAlive).

## Version 1 features

Each maps to a Rancher Desktop screen people use most.

### 1. General (home)
- Colima status per profile: running or stopped, runtime, architecture, CPUs, memory, disk,
  Kubernetes on or off, address. From `colima list --json` and `colima status --json -p <profile>`.
- Start, stop and restart, with live output streamed from the CLI while it runs (the first start
  can take minutes).
- Disk use from `docker system df`: images, containers, volumes, build cache, reclaimable.
- Docker context check: is the active Docker context this Colima profile? Offer the
  `docker context use colima` fix as a copyable command.

### 2. Containers
- List running and stopped containers: name, image, state, uptime, published ports as clickable
  `http://localhost:<port>` links, CPU and memory (one `stats` sample, not a stream, to stay light).
- Start, stop, restart, pause and remove (remove asks in the page, never with a browser dialog).
- Logs: live tail over Server-Sent Events, with follow, search and timestamps.
- Inspect: environment (values masked by default, shown on click), mounts, ports, networks, labels,
  restart policy.
- Group by Compose project (`com.docker.compose.project` label), with start and stop for the group.

### 3. Images
- List: repository, tag, size, created, in use or not.
- Pull by name with live progress; remove; prune unused (shows space reclaimed first).
- Scan for vulnerabilities with Trivy **when `trivy` is installed** (Rancher Desktop's image scan).
  Otherwise the button explains how to install it. No bundled scanner.

### 4. Volumes
- List with size and which containers use each; remove; prune unused.

### 5. Kubernetes
- On or off (restarts Colima with `--kubernetes`), k3s version, node status.
- kubectl context check (is `colima` the current context?), with the copyable fix.
- Version 1 stops there. Workloads and port forwarding are version 2.

### 6. Preferences (Rancher's "Virtual Machine" and "Container Engine" pages)
- CPUs, memory, disk, runtime (docker or containerd), VM type (vz or qemu), Rosetta for amd64,
  mount type, Kubernetes. Shows the current values, saves through `colima start --save-config`
  with the new flags, and says plainly that it restarts the VM.
- Disk can only grow; the form says so and refuses a smaller value.
- Profiles: list, create (with the settings above), delete with in-page confirmation.

### 7. Troubleshooting
- Colima and VM logs (`~/.colima/<profile>/` and `colima ssh -- journalctl`), tail and download.
- Diagnostics, each a pass or fail with its fix: colima on PATH, VM running, Docker socket answers,
  Docker context, kubectl context, free disk on the Mac, Colima version against the latest release.
- Prune cached downloads (`colima prune`); factory reset of a profile (`colima delete`) behind a
  typed confirmation.

### Version 2 candidates (not in version 1)
- Exec into a container in the browser (xterm.js over a WebSocket).
- Kubernetes workloads, logs and port forwarding.
- containerd runtime screens through `colima nerdctl`.
- Menu bar status item; notifications when a container exits.
- Homebrew tap.

## Architecture

```
browser ──HTTP + SSE──> colima-ui (Go, 127.0.0.1:7766)
                          ├─ Docker Engine API over the profile's socket
                          │    ~/.colima/<profile>/docker.sock
                          └─ colima CLI (exec, --json where offered)
```

- **Go 1.27**, standard library `net/http`. Talk to the Docker Engine API directly over the Unix
  socket with a small client of our own (only the endpoints above); no Docker SDK, which would
  multiply the binary size.
- **Front end:** plain HTML, CSS and JavaScript embedded with `embed.FS`; no build step, no npm.
  One page with tabs; light and dark from `prefers-color-scheme`.
- **Live output:** Server-Sent Events for logs, pulls and colima start and stop output.
- **Colima calls:** `exec.CommandContext` with argument lists, never a shell string; profile names
  validated against `^[a-zA-Z0-9_-]+$`.
- **Layout:** `cmd/colima-ui/main.go`, `internal/server` (HTTP, auth), `internal/docker` (socket
  client), `internal/colima` (CLI wrapper), `web/` (embedded assets).

## Security

The Docker socket controls everything in the VM, and any web page the user visits can send requests
to localhost. So:

- Listen on `127.0.0.1` only. No flag to change that in version 1.
- A random token is generated at each start and passed in the URL opened in the browser; the page
  swaps it for an `HttpOnly`, `SameSite=Strict` cookie. Every API call needs it.
- Reject requests whose `Host` is not `127.0.0.1:<port>` or `localhost:<port>` (DNS rebinding).
- Reject state-changing requests whose `Origin` is not the UI's own.
- A strict Content-Security-Policy; no third-party scripts or fonts.
- Destructive actions (remove, prune, delete profile) confirm in the page.

## Testing

- Unit tests for the Docker client and the colima wrapper against recorded responses (no VM needed).
- Handler tests for the auth, Host and Origin checks.
- An integration test, skipped unless `COLIMA_UI_IT=1`, that runs against a real Colima profile.
- Manual check in the browser on a stopped and a running profile before each release.

## Blog numbers to measure

For the post on lightweight containers on a MacBook, measure on the same Mac, same workload:
- Idle memory and CPU of Rancher Desktop (app plus VM) against Colima plus Colima UI.
- Install size, cold start time to a working `docker run hello-world`.
- Battery drain over an hour idle, if practical.
Rancher Desktop is no longer installed here (its settings folders remain); reinstall it temporarily
to measure, or use published figures and say so.

## Release

- Apache 2.0.
- GitHub releases with a darwin/arm64 and darwin/amd64 binary, built by GitHub Actions.
- No GitHub repo yet: create and push only when the owner says so.
