# Measurement plan: Colima vs Rancher Desktop on a MacBook

Status: agreed with the owner, 17 Nov 2026. Feeds the blog post and the gap list Colima UI sets out to fill.

## The question the post answers

What does a MacBook developer give up, and what do they get back, by replacing Rancher Desktop with
plain Colima? Where are the gaps, and how much of them does a small open source UI close without
bringing back the weight?

## Test bed

| Item | Value |
|---|---|
| Mac | Apple M5, 10 cores, 32 GiB, macOS 27.2.1 |
| Colima | 0.10.3 (Homebrew), VM type `vz`, mount `virtiofs` |
| Rancher Desktop | 1.25.0 (Homebrew cask `rancher`), VM type `vz`, engine `moby` |
| Docker CLI | Homebrew, same binary for both (Rancher's own `~/.rd/bin/docker` noted, not used) |
| Power | On AC for timing runs; battery only for the optional drain test |

Rules for every run:
- Only one of the two runs at a time; the other is fully stopped (no VM process left).
- Background load is recorded with every sample (load average, top five processes), so a noisy run
  can be spotted and repeated. Other Claude sessions run on this Mac; noise is expected and stated.
- Each timed test runs 5 times; the post reports the median and the range.
- Scripts and raw CSVs live in `bench/` and are published with the repo, so anyone can rerun them.

## Decisions (owner, 17 Nov 2026)

- Configurations: out of the box, matched, and matched plus Kubernetes (all three below).
- Every metric, including memory, CPU, power (`powermetrics`) and a one hour battery test per tool.
  `powermetrics` needs root: either the owner runs it when asked, or adds a narrow sudoers rule
  for `/usr/bin/powermetrics` only. The battery test needs the Mac unplugged, one hour per tool
  plus one hour baseline.
- The old 59 GiB Rancher data is moved aside now and deleted at the end, after the owner confirms.
- The post is drafted as Markdown in `docs/blog/`; the repo is created private under
  `andrewbakercloudscale/colima-ui` and made public when the post goes live.

## Configurations

1. **Out of the box.** Each tool with its own defaults (Rancher: Kubernetes on, its default memory
   and CPUs; Colima: 2 CPUs, 2 GiB, no Kubernetes). This is what a new user actually gets.
2. **Matched.** Same CPUs, memory and disk, Kubernetes off, Docker engine on both: 4 CPUs, 8 GiB,
   100 GiB (`CPUS=4 MEMORY_GB=8`). Rancher's defaults already equal Colima's 2 CPUs and 2 GiB, so
   a larger matched VM adds information instead of repeating the out of the box run. This isolates
   the cost of the tool itself (Electron app, extra services) from the size of the VM.
3. **Matched plus Kubernetes.** As 2 with Kubernetes on (k3s in both), since that is Rancher's
   headline feature.

## Metrics

| # | Metric | How | Tool |
|---|---|---|---|
| M1 | Download size | Size of the cask download / `brew fetch` | both |
| M2 | Installed footprint | App bundle, `~/Library` data, VM disk image after first start | both |
| M3 | First start | Install done to first `docker run --rm hello-world` success, incl. VM image download | both |
| M4 | Cold start | VM stopped to `docker run --rm hello-world` success, 5 runs | both |
| M5 | Stop time | Stop command to no VM process | both |
| M6 | Idle memory | Sum of host processes (app, helpers, Lima host agent, VZ VM process) by physical footprint, sampled every 5 s for 10 min after a 5 min settle | both |
| M7 | Idle CPU | CPU % and accumulated CPU seconds over the same window | both |
| M8 | Energy and power | `top -stats power` energy impact (no sudo) every run, plus `powermetrics` CPU/GPU/ANE milliwatts and per-process energy, baselined against the Mac with neither tool running | both |
| M9 | Build time | Build one small sample image with a cold and a warm cache | both |
| M10 | Bind mount I/O | Unpack a large source tree and run a file-heavy step on a host folder mounted in a container | both |
| M11 | Kubernetes ready | Kubernetes on: start to `kubectl get nodes` Ready; idle memory with it on | both |
| M12 | Battery drain | One hour idle on battery, lid open, screen on, percentage before and after | both |
| M13 | UI cost | Memory and CPU of the GUI alone: Rancher's Electron processes vs `colima-ui` | both |
| M14 | Leftovers | What remains on disk after uninstall | Rancher (59 GiB found on this Mac) |

Process sets measured (by name, refined once Rancher is installed):
- Rancher Desktop: `Rancher Desktop` and its Helper processes, `limactl` host agent, the VZ VM
  process, `rdctl`/`docker-credential` helpers if present.
- Colima: `colima` daemon, `limactl` host agent, the VZ VM process, and `colima-ui` once built.

## Gap analysis (the second half of the post)

A feature table, three columns: Rancher Desktop 1.25, Colima 0.10.3 CLI, Colima UI v1. Rows come
from Rancher's documented features, each checked by hand in the reinstalled app, not from memory:
- GUI and menu bar status, start and stop, live progress
- Containers, images, volumes screens; logs; exec; Compose grouping
- Port forwarding UI, Kubernetes version picker, Kubernetes reset, dashboard
- Image scanning (Trivy), extensions, snapshots, diagnostics, troubleshooting reset
- Preferences: CPUs, memory, VM type, mounts, Rosetta, proxy, admin access, PATH management
- Docker context and kubectl context handling (Colima creates its `colima` Docker context on start
  and removes it on stop, so a stopped Colima leaves `docker` pointing at nothing useful; the UI
  diagnostics check the active context)
- Updates, uninstall, leftovers

Each gap is marked: closed by Colima UI v1, planned for v2, or left alone on purpose (with why).

## Keeping this Mac safe while Rancher is back

Rancher Desktop changes shell and Docker settings. Before installing:
- Back up `~/.zshrc`, `~/.bash_profile`, `~/.zprofile`, `~/.docker/`, `~/.kube/` to
  `~/colima-bench-backup/<date>/`.
- Move the old 59 GiB `~/Library/Application Support/rancher-desktop` aside (a rename, instant) so
  the install is genuinely fresh and M2/M3 are honest.
- Pre-seed a Rancher deployment profile so the first run asks nothing: admin access off (no
  `/var/run/docker.sock` takeover, no sudo prompt), PATH management manual (no shell edits),
  engine `moby`.
- Stop Colima before starting Rancher and the reverse; never both at once.

After measuring: quit and uninstall Rancher, restore the backups, check `docker` works against
Colima again, and report what Rancher left behind (M14).

## Deliverables

1. `bench/` scripts (bash, one per metric group) and `bench/results/*.csv` with the raw samples.
2. `docs/blog/results.md`: tables and charts from the CSVs.
3. `docs/blog/draft.md`: the post, with the gap table and the Colima UI section.
4. Colima UI v1 built against the gaps, with screenshots for the post.
5. The public GitHub repo under `andrewbakercloudscale/colima-ui` when the owner says so.

## Order of work

1. Write and dry-run the bench scripts against Colima (no Rancher needed yet).
2. Back up, reinstall Rancher, run every Rancher metric, capture the gap table by hand.
3. Uninstall Rancher, restore, rerun the Colima metrics under the same conditions.
4. Build Colima UI v1 (see `docs/PLAN.md`), measure M13 for it.
5. Write the results page and the draft post.
