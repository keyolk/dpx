# dpx

Terminal browser for Doppler projects, configs and secrets.

```
dpx                              # interactive browser
dpx projects [filter]            # list projects
dpx configs <project>            # list a project's configs
dpx secrets <project> <config>   # list secret names
dpx get <project> <config> <key> # print one value to stdout
dpx open [project] [config]      # open the dashboard page in a browser
```

## Authentication

Three sources, in order:

1. A [`pass`](https://www.passwordstore.org/) entry named in `~/.config/dpx/config.yaml`.
2. `$DOPPLER_TOKEN`.
3. The token store the official `doppler` CLI writes to `~/.doppler/.doppler.yaml`.

Nothing new to provision for 2 and 3: a machine already logged into `doppler`
runs dpx as-is. Scoped tokens are resolved by longest matching directory
prefix, the same way the CLI does — a repo pinned to another workplace stays
pinned under dpx too.

### Reading the token from `pass`

```yaml
# ~/.config/dpx/config.yaml
pass-entry: sendbird/doppler.com/token/gavin.jeong

# optional: a directory that belongs to another workplace
scoped:
    /Users/me/src/other-org: other-org/doppler/token

# optional: a self-hosted instance
# api-host: https://api.doppler.example
# dashboard-host: https://dashboard.doppler.example
```

The token is read from the first line of the entry, so the usual
password-plus-metadata layout works unchanged.

This source is checked **before** `$DOPPLER_TOKEN` on purpose. A shell profile
that exports `DOPPLER_TOKEN` globally would otherwise shadow the entry you
configured, making it dead config. A named entry that cannot be read is an
error rather than a fallback — browsing a different workplace than the one you
pinned is worse than failing.

`pass` runs attached to the terminal, so a locked GPG key prompts normally.
Set `$DPX_PASS_BIN` to use a compatible binary under another name.

## Caching

The Doppler API returns weak ETags on every listing endpoint and honors
`If-None-Match`. dpx stores each listing next to its ETag, so a refresh is a
revalidation, not a re-download.

Measured against a workplace of a few hundred projects:

| | time | transferred |
|---|---|---|
| cold walk | ~1.3s | tens of KB |
| revalidation (`dpx refresh`) | ~0.3s | 0 bytes of payload |
| cached read (`dpx projects`) | ~10ms | nothing |

Cached pages are revalidated concurrently, since with no bodies to transfer the
cost is entirely round-trip latency.

`--refresh` skips the TTL but keeps the ETags — it means "do not trust the age",
not "throw the cache away". `dpx cache clear` is the genuine cold start;
`dpx cache info` shows what is held.

Per project, environments and configs are fetched only when you navigate into
it, so the first run costs one listing request rather than one per project.

## Secrets

Navigating shows **names only**. Values are fetched when you ask for them
(`s` for one, `S` for the whole config) and are **never written to disk** —
the cache holds names, ETags and structure, nothing else. The cache file is
mode 0600.

`dpx get` prints a value to stdout with no decoration, so it composes:

```sh
export DB_PASSWORD=$(dpx get my-project prd DB_PASSWORD)
```

## Keys

| key | action |
|---|---|
| `j`/`k`, `↓`/`↑` | move |
| `enter`, `l` | open (on secrets: toggle detail) |
| `esc`, `h` | back |
| `/` | fzf-style filter |
| `r` | revalidate the current level |
| `s` / `S` | reveal the selected value / the whole config |
| `yc` / `yv` | copy the name / the secret's value (fetched if needed) |
| `m` | who can access this project |
| `enter` / `x` | on access: change role / revoke |
| `o` | open the dashboard page for whatever the cursor is on |
| `?` | help |
| `q` | quit |

## Access control

`m` on a project shows who can reach it — people and groups alike, with the
role each holds and which environments the grant covers. Group rows are marked,
because revoking one takes access from everyone in it.

`enter` opens the role picker, listing the roles the workplace actually
defines including custom ones; `x` revokes. Both are confirmed before they
run, as is any promotion to `admin` or `owner` — the changes that grant the
most are the easiest to make by accident from a list.

## Opening the dashboard

`o` in the browser, or `dpx open` from the shell, opens the Doppler dashboard
page for what you are looking at — the highlighted project on the project list,
the highlighted config below that. The secrets screen opens its config, since
the dashboard has no per-secret page.

`$BROWSER` is honored. `dpx open --print` writes the URL instead of opening it.
The dashboard host comes from the same config the token does, so a self-hosted
instance opens its own dashboard rather than the public one.

## Build

```sh
make build      # -> bin/dpx
make install    # -> ~/.local/bin/dpx
make test
```

Set `DPX_ASCII=1` to force ASCII glyphs; `NO_COLOR` is respected.
