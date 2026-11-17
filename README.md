# Colima Dashboard

A lightweight, local web UI for [Colima](https://github.com/abiosoft/colima) on macOS: the everyday
screens of a desktop container app (status, containers, images, volumes, Kubernetes, preferences and
troubleshooting) in one small binary, opened in your browser.

**Status:** version 1 in progress, not released. See [docs/PLAN.md](docs/PLAN.md).

## Run it

```sh
go build -o colima-dashboard ./cmd/colima-dashboard
./colima-dashboard            # opens http://127.0.0.1:7766 signed in
./colima-dashboard open       # sign a browser in again
./colima-dashboard install-service   # start at login
```

Screens: General, Monitor (live graphs per VM and per container), Containers, Images, Volumes,
Kubernetes, Preferences and Troubleshooting.

- Runs at `http://127.0.0.1:7766`, on this Mac only.
- One Go binary with the page built in; no Electron, no Node, no runtime to install.
- Talks to Colima through its CLI and to Docker through Colima's socket. Changes nothing until you
  press a button, and asks in the page before anything destructive.

Colima Dashboard is an independent project. It is not affiliated with or endorsed by Colima, Rancher
Desktop or SUSE.

## Credits

Some ideas, not code, came from reading two other MIT-licensed Colima front ends:
[vnknowledge2014/colima-ui](https://github.com/vnknowledge2014/colima-ui) (finding the Colima
folder, the PATH a LaunchAgent gets) and [jsynowiec/ColimaUI](https://github.com/jsynowiec/ColimaUI).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
