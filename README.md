# tokgo

Records TikTok live streams to mp4. A clean-room Go implementation of the CLI
surface of an earlier Python implementation:
same flags, same three modes, no Python runtime, plus an interactive `tokgo tui`
front end.

```
tokgo -user somecreator -mode automatic -output ./captures
```

## Requirements

- Go 1.24+ to build
- `ffmpeg` on `PATH` (or pass `-ffmpeg-path`)

```bash
go build -o tokgo .
```

## Flags

| Flag | Description |
|------|-------------|
| `-url <URL>` | TikTok live URL (`https://www.tiktok.com/@name/live`, `https://vm.tiktok.com/...`) |
| `-user <NAMES>` | Username(s), comma separated, `@` optional |
| `-room_id <ID>` | Numeric room id, bypasses username resolution |
| `-mode <MODE>` | `manual` (default), `automatic`, `followers` |
| `-automatic_interval <MIN>` | Minutes between liveness polls (default 5, minimum 1) |
| `-duration <SECONDS>` | Stop after this many seconds per session |
| `-output <DIR>` | Output directory |
| `-bitrate <RATE>` | Re-encode video at this bitrate (`1000k`, `1M`) instead of stream copy |
| `-proxy <URL>` | HTTP proxy for lookups; the video transfer always goes direct |
| `-telegram` | Upload the finished file to Telegram |
| `-ffmpeg-path <PATH>` | Custom ffmpeg binary |
| `-no-update-check` | Skip the release check |

Passing one of `-user`, `-room_id`, `-url` is required for `manual` and
`automatic`; passing more than one is an error.

## TUI

```
tokgo tui
```

An interactive form for the same run, built on Bubble Tea: mode chips, a single
target field that accepts a username, a live URL or a numeric room id, plus
duration, output directory, proxy and the Telegram toggle. The form is turned
into argv and validated by the same `cli.Parse` the flags use, so it cannot
accept a combination the CLI would reject or miss one it allows.

Start swaps to a live view: target, mode, elapsed time against the `-duration`
cap, and the tail of the runner's log. Ctrl-C stops the capture and lets the
remux finish (a second press quits immediately), `r` starts a new run with the
form still filled in. The UI owns stdout while it runs, so log lines are routed
into the view; the debug log file is written as usual.

## Modes

**manual** resolves the room once, checks that it is live, records until the
stream ends, the room goes offline, `-duration` is reached, or you press Ctrl-C.

**automatic** polls the user every `-automatic_interval` minutes and records
each session it catches, forever.

**followers** lists the accounts you follow and records every one of them that
is live, each in its own goroutine. It needs a logged-in session, so
`cookies.json` must contain `sessionid_ss`; without it the run stops
immediately instead of polling a stranger's follower list (TikTok's anonymous
pages embed a random `secUid`). A follower is not started twice while its
recording is running.

Ctrl-C once stops the capture, finalises the file and runs the mp4 remux.
Ctrl-C twice aborts immediately, leaving a partial `_flv.mp4`.

## Output

Files land as `TK_<user>_<YYYY.MM.DD_HH-MM-SS>_flv.mp4`. When the recording
finishes the raw container is remuxed to `TK_<user>_<...>.mp4` with
`ffmpeg -c copy` (stream copy, no quality loss) and the `_flv.mp4` original is
deleted. With `-bitrate` the video is re-encoded with libx264 while audio is
copied.

If every advertised CDN endpoint yields fewer than 4 KiB, no file is left
behind and the run fails with `unable to retrieve the live stream url`.

## Cookies and Telegram

`cookies.json` is a flat JSON object of cookie name to value, read from the
working directory. `sessionid_ss` is the one that carries a login:

```json
{ "sessionid_ss": "..." }
```

Cookies are sent to both `www.tiktok.com` and `webcast.tiktok.com`; the per-host
cookie jar would otherwise leave the webcast API unauthenticated, which is
exactly the endpoint a login-gated room needs. A missing file is fine; TikTok's
login walls are then reported per request.

`tt-target-idc` and `tt-target-idc-sign` are **ignored** if present. They are
routing hints TikTok computes per session, so a value from a config file can only
be stale. Measured against a login-gated room with a valid `sessionid_ss`:

| `tt-target-idc` sent | `room/info` |
| -------------------- | ----------- |
| none                 | `0`, manifest present |
| `useast2a`           | `4003110`, no manifest |
| `useast1a`           | `0`, manifest present |
| `useast8`            | `0`, manifest present |
| `my2`                | `0`, manifest present |
| `sg2`                | `0`, manifest present |
| `eu1`                | `0`, manifest present |

So the cookie is never needed, and `useast2a` — the value the reference project
ships in its default `cookies.json` — is the one value that breaks room lookups.
Guessing a datacentre is not worth a retry ladder: every value except that one
behaves exactly like sending nothing. The recorder strips these cookies from
every request and warns once when the config mentions one.

Login-gated rooms are fatal in every mode: the run stops with
`live is private, login required` and exit status 1 instead of polling a room no
retry can unlock. Only genuinely retryable conditions (not live yet, empty CDN,
WAF block, unknown user) keep the automatic loop alive.

`telegram.json` for `-telegram`:

```json
{ "bot_token": "...", "chat_id": "123456", "caption": "optional" }
```

## How it works

Resolution order, per mode, against TikTok's own endpoints:

1. `POST`-free lookups only: `tikrec.com/tiktok/room/api/sign` returns a signed
   `www.tiktok.com/api-live/user/room` URL whose `X-Bogus`/`X-Gnarly`
   parameters TikTok accepts. If that service is down, the recorder falls back
   to the unsigned `tiktok.eulerstream.com/webcast/room_info` endpoint.
2. `webcast.tiktok.com/webcast/room/check_alive` establishes liveness before
   any room data is trusted, so an ended room that still advertises stale
   stream URLs is not recorded.
3. `webcast.tiktok.com/webcast/room/info` yields the stream manifest. Each
   quality carries a numeric `level`; endpoints are tried best-first (origin,
   uhd, hd, sd, ld) for both FLV and HLS, then the legacy `flv_pull_url` tiers
   and finally RTMP.
4. The chosen endpoint is copied to disk through a 512 KiB buffer, re-checking
   liveness between reconnects. An endpoint that delivers nothing is abandoned
   in favour of the next candidate.

When TikTok answers `status_code 4003110` (WAF or region restriction) the
recorder falls back to the signed web UI endpoint, which carries the same stream
manifest, and only then to scraping the public live page.

## TLS impersonation and proxies

TikTok's WAF fingerprints the TLS handshake, so every connection is made with a
Chrome ClientHello via [utls](https://github.com/refraction-networking/utls)
instead of Go's `crypto/tls`. Server-observed fingerprints:

| Client                       | JA4                                      |
| ---------------------------- | ---------------------------------------- |
| Go `crypto/tls`              | `t13d1311h2_f57a46bbacb6_e7c285222651`   |
| this tool (utls, Chrome 120) | `t13d1516h1_8daaf6152771_02713d6af862`   |

The second value carries Chrome's cipher and extension hashes (`8daaf6152771`,
`02713d6af862`). Three deliberate details:

- The profile is pinned to `HelloChrome_120` rather than `HelloChrome_Auto`.
  Auto tracks the newest Chrome, which sends post-quantum key shares that some
  middleboxes drop, and it drifts away from the `User-Agent` silently. The
  header set is Chrome 120 to match.
- ALPN is narrowed to `http/1.1`. utls presets carry their own `h2, http/1.1`
  list and ignore `Config.NextProtos`; left alone, a server would agree on `h2`
  while Go's HTTP/1.1 transport talked past it. The reference tool forces
  HTTP/1.1 for the same reason.
- The proxy is tunnelled inside the dialer, with `Transport.Proxy` left nil.
  `http.Transport` ignores `DialTLSContext` for proxied requests, so a proxied
  run would otherwise silently fall back to Go's fingerprint. `-proxy` therefore
  gets impersonation too, including `Basic` credentials in the `CONNECT`.

## Divergences from the Python original

Deliberate, and each one is a decision rather than an omission:

- **Telegram uses the Bot API**, not MTProto. A user-account client would mean a
  multi-megabyte MTProto dependency and a session store; a bot token plus
  `sendDocument` covers the same need in ~90 lines.
- **`-duration` is a total budget** for one recording, not a per-connection
  timer. The original resets the timer on every reconnect, so a flapping stream
  over-records.
- **The update check does not self-replace.** The original downloads a zip and
  overwrites its own source tree. A compiled binary has nothing to overwrite, so
  the check only reports the latest tag, and only when `updateRepo` is set at
  build time.
- **Multi-user and followers recordings are goroutines** sharing one process
  rather than forked processes, so one Ctrl-C reaches all of them.
- **`signed_url` is honoured.** The live tikrec response returns a `signed_url`
  (absolute); the Python code looks for `signed_path` and would build a broken
  URL. Both keys are accepted here.
- **HTTP/1.1 only.** The reference also forces `http_version="v1"`; there is no
  HTTP/2 path here.

## Tests

```bash
go test ./...          # offline: fake network, no ffmpeg, no TikTok
go test -race ./...
go test -tags live ./...   # real TikTok, real fingerprints, real proxy tunnel
```

Unit tests cover the quality ordering, the CLI validation matrix, the recorder
fallback and stop logic (with a fake network), ffmpeg argument construction, the
Telegram multipart request, the `CONNECT` tunnel framing (including bytes a
proxy sends in the same packet as its `200`), and a utls handshake against a Go
TLS server. Offline tests never touch the network.

The `live` tag adds four checks that only make sense against the real service:
the JA4 fingerprint is Chrome's and not `crypto/tls`'s, a Chrome hello completes
against `www.tiktok.com`, a request survives the hand-rolled `CONNECT` tunnel to
TikTok, and the same signed room URL works through both TLS stacks.

## Verified against the live service

Recorded on 2026-10-29 against `@aljazeeraenglish` and `@tiktok`:

- `-user` → tikrec sign → room id → liveness → capture: `TK_aljazeeraenglish_*.mp4`,
  ffprobe `h264 1280x720 25fps + aac`, 2.0 MB for 8.18 s.
- 60 s cap with `-race`: exit 0, no data race, 62.19 s container (the CDN pushes
  roughly 2 s of buffered media on connect), 1.93 Mbps, 15 MB.
- `-bitrate 800k`: re-encoded to 15.08 s / 1.01 Mbps, video and audio present.
- `-url`, `-room_id`: both resolve to the same room and record.
- Ctrl-C once: capture finalised and remuxed to `.mp4`, `_flv.mp4` removed.
- `-mode automatic`: records, then reports the wait before the next poll.
- Multi-user: both users run concurrently, one live one not; the live one lands,
  the dead one is reported once with its `@name` prefix, exit status 1.
- TLS impersonation: JA4 `t13d1516h1_8daaf6152771_02713d6af862` reported by
  `tls.peet.ws`, against `t13d1311h2_f57a46bbacb6_e7c285222651` for `crypto/tls`;
  `www.tiktok.com` negotiates `http/1.1` with that hello; a capture re-taken
  through the new transport produced 3.4 MB / 14.2 s.
- Error paths: not-live, invalid URL, mutually exclusive flags, and TikTok's WAF
  answer (`403` with an empty body) all report a single clear message.

### Restricted lives

A `4003110` answer means TikTok refuses the webcast room API for that room
(login or region restriction). Two fallbacks are tried, in this order:

1. **The signed web UI endpoint.** `tikrec.com/tiktok/room/api/sign` signs a
   `www.tiktok.com/api-live/user/room` request, which returns the same stream
   manifest the webcast API would have (`data.liveRoom.streamData.pull_data`).
   The recorder maps it onto the ordinary quality ordering, so a restricted room
   gets the same best-first CDN list as any other.
2. **The public live page**, scraping a `.flv`/`.m3u8` url out of the HTML.

The second is now unreliable: TikTok answers `GET /@user/live` with a 1,485-byte
SlardarWAF challenge stub that contains no stream urls, with or without a warmed
cookie jar. The reference tool relies on that scrape alone, which means its
restricted-LIVE support is effectively dead against the current site. Hence the
signed endpoint became the primary fallback.

Verified two ways:

- `TestRestrictedLiveIsCapturable` walks the whole chain offline: `RoomAlive`
  reports a restricted room alive, `LiveURLs` resolves the manifest through the
  signed endpoint and orders origin before sd, and `OpenStream` returns the
  media bytes. `TestRestrictedLiveFallsBackToPageScrape` covers the second path.
- Against the real service, the signed fallback was checked directly:
  `TestSignedEndpointYieldsPlayableStream` resolved 3 candidate urls for a live
  user and the first one served an FLV container.
- A real restricted live was found and recorded (`@donut_ntp.42`, room
  `7701946275792554760`):
  `check_alive` says `alive:true`, `room/info` answers `4003110` with an empty
  prompt, the signed endpoint returns the room with `status:2` but **no
  `streamData` key at all**, the live page is the SlardarWAF stub, and the
  Android webcast host answers `4003110` too. TikTok withholds that manifest from
  anonymous clients on every reachable endpoint, so the chain correctly ends in
  `live is private, login required` rather than a wrong `not live`. With a valid
  `sessionid_ss` the same room records (1.0 MB, 6 s, h264 + aac), and the run
  logs the one-off retry that drops the stale `tt-target-idc` cookie.
