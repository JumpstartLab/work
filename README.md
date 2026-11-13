# work

The front door to your Jumpstart Lab dev box. Sign in once with your own
account; after that `work` finds your box, wakes it if it has gone to sleep, and
drops you into its persistent `work` tmux session over your own tailnet.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/JumpstartLab/work/main/install.sh | sh
work login
work
```

You also need [Tailscale](https://tailscale.com/download) signed in to the
tailnet your box joined, and optionally [Eternal Terminal](https://eternalterminal.dev)
(`brew install MisterTea/et/et`) for a connection that survives sleep and
network changes. Without `et`, `work` uses `ssh`.

## Commands

| | |
|---|---|
| `work login` | Sign in (device code → your browser → your Google account) |
| `work` / `work <box>` | Wake the box if needed, attach its `work` tmux session |
| `work list` | Your boxes and their state |
| `work code <box> [dir]` | VS Code on this machine, Remote-SSH'd into the box |
| `work doctor` | Check sign-in, Switchboard, Tailscale, et, and reachability |
| `work logout` | Forget the sign-in |

## How it works

- **Sign-in:** OAuth 2.0 device authorization grant against
  `identity.jumpstartlab.com` (Zitadel app "work CLI" in the Switchboard
  project). Tokens live in `~/.config/work/token.json` (0600) and refresh
  themselves.
- **Your boxes:** Switchboard's `boxes.mine`, `boxes.status` and `boxes.wake`
  (via the stateless `/mcp` `invoke` meta-tool). Switchboard decides ownership
  from your verified email; the box itself lives on your tailnet, and
  Switchboard is the only thing that can start it for you.
- **Connect:** `et <user>@<box> -c "tmux new-session -A -s work"`, falling back
  to `ssh -t`.

## Develop

```sh
go test ./...
WORK_SWITCHBOARD_URL=http://localhost:3000/mcp go run . list
git tag v0.1.0 && git push --tags   # release.yml builds and publishes binaries
```
