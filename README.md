# work

The front door to your Jumpstart Lab dev box. Sign in once with your own
account; after that `work` finds your box, wakes it if it has gone to sleep, and
drops you into its persistent `work` tmux session over your own tailnet.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/JumpstartLab/work/main/install.sh | sh
work login you@yourcompany.com
work
```

You also need [Tailscale](https://tailscale.com/download) signed in to the
tailnet your box joined. `work` reconnects on its own when your laptop sleeps or
changes networks (your session lives in tmux on the box, so nothing is lost), and
on macOS it keeps your SSH key's passphrase in the Keychain after the first ask.
If [Eternal Terminal](https://eternalterminal.dev) is installed, `work` uses it
instead.

## Commands

| | |
|---|---|
| `work login <email>` | Sign in (device code → your browser → your Google account). The email picks your organization; leave it off only for a personal address that belongs to none |
| `work bus enroll [name]` | Put this machine on your own private party-line bus, so the Claude sessions on your laptop and your box can message each other (and nobody else's). Defaults to this computer's name; run it again to rotate the token |
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
