# Colima UI

A lightweight, local web UI for [Colima](https://github.com/abiosoft/colima) on macOS: the everyday
screens of a desktop container app (status, containers, images, volumes, Kubernetes, preferences and
troubleshooting) in one small binary, opened in your browser.

**Status:** planning. See [docs/PLAN.md](docs/PLAN.md) for the version 1 features.

- Runs at `http://127.0.0.1:7766`, on this Mac only.
- One Go binary with the page built in; no Electron, no Node, no runtime to install.
- Talks to Colima through its CLI and to Docker through Colima's socket. Changes nothing until you
  press a button, and asks in the page before anything destructive.

Colima UI is an independent project. It is not affiliated with or endorsed by Colima, Rancher
Desktop or SUSE.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
