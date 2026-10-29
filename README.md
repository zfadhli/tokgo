# tokgo

Records TikTok live streams to mp4. A clean-room Go implementation of the CLI
surface of an earlier Python implementation:
same flags, same three modes, no Python runtime, no third-party dependencies.

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
working directory:

```json
{ "sessionid_ss": "...", "tt-target-idc": "useast2a" }
```

A missing file is fine; TikTok's login walls are then reported per request. For
private or login-restricted lives, supply the cookies.

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
recorder scrapes the public live page HTML for a direct `.flv`/`.m3u8` URL
before giving up.

## Divergences from the Python original

Deliberate, and each one is a decision rather than an omission:

- **No TLS impersonation.** `curl_cffi` mimics Chrome's TLS fingerprint. Go's
  standard library cannot, so requests carry Chrome's header set only. The
  endpoints this tool uses do not currently require the fingerprint; a future
  WAF change would be handled by the page-scrape fallback.
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

## Tests

```bash
 go test ./...
 go test -race ./...
```

Unit tests cover the quality ordering, the CLI validation matrix, the recorder
fallback and stop logic (with a fake network), ffmpeg argument construction, and
the Telegram multipart request. No test touches the network.

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
- Error paths: not-live, invalid URL, mutually exclusive flags, and TikTok's WAF
  answer (`403` with an empty body) all report a single clear message.
