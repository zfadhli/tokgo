# Spec: TS intermediate capture, mp4 on end/cancel

## Request
"Download as .ts and remux to mp4 when livestream ended or when user cancel download."
The remux-at-end half already exists (`record.Convert`, `context.WithoutCancel` + 30 min).
The delta is the container written **during** capture.

## Measured evidence
| claim | measurement |
|---|---|
| Raw capture today | file magic `FLV\x01\x05`; named `_flv.mp4` — extension lies |
| Truncated FLV | `format_name=flv duration=0.000000` — unplayable/unseekable without a remux pass |
| Truncated TS (60% of bytes) | `format_name=mpegts duration=2.993000` |
| SIGKILL mid-write TS (ffmpeg -re, kill -9 at 4.5 s) | `mpegts duration=4.379`, h264 + aac, remuxes to mp4 `duration=4.160` |
| FLV → TS on a pipe | works, `start_time` rebased 28119 → 1.407, FLV ts-0 discontinuity flattened |
| Concatenated FLV over pipe (reconnect case) | ffmpeg logs "Concatenated FLV detected", recovers, 218 frames |
| 3 s cap → ~5 s file (open bug) | **not a deadline bug.** 10 s capture → 11.04 s of continuous media (delta-summed), one ts-0 → 28170040 ms jump at tag 3. CDN sends a keyframe-aligned lead-in |

## Decision
Live ffmpeg pipe: capture bytes flow through `ffmpeg -i pipe:0 -c copy -f mpegts out.ts`;
at end/cancel remux `out.ts` → `.mp4` and drop the `.ts` (keep it if the remux fails).

Architect dissent (rejected, with mitigations recorded): "capture stops being self-sufficient —
dead ffmpeg means zero bytes". Mitigated by (a) muxer start failure falls back to raw `.flv`
file capture for that candidate, (b) a muxer that dies mid-capture leaves a valid partial TS,
which is strictly better than today's fallback. Rejected alternatives: rename-only `.flv`
(does not deliver crash-tolerant TS), HLS byte-copy (needs url re-ranking in internal/tiktok and
deletes the reconnect loop), sidecar tailing ffmpeg (two writers, offset churn).

## Revision after measurement: one muxer per connection
The first draft said one muxer per candidate url. Measurement changed that.

| shape | video duration | audio duration |
|---|---|---|
| today: both connections appended into one file | 11.336 s | 22.058 s |
| one long-lived muxer over a pipe | 11.296 s | 22.011 s |
| one muxer per connection + concat demuxer | **22.259 s** | 22.208 s |

Every reconnect today silently drops the second connection's video: the new FLV restarts its
timestamps and ffmpeg discards the colliding video packets while keeping the audio. Per-connection
`<base>.NNN.ts` segments joined with `-f concat -safe 0 -i list.txt -c copy` recover both streams,
with a clean seam (554 video packets, 0 backward DTS jumps, first -0.021, last 22.198).

Consequence: this change also fixes a pre-existing data-loss bug, and crash durability improves —
a kill leaves all earlier segments complete and the newest truncated but valid.

## Invariants that must survive
1. `copyStream` interrupt closes **both** body and writer. `body.Close()` alone cannot unblock a
   pending write into a stalled stdin pipe — that is the `-duration` / Ctrl-C guarantee.
2. Muxer close is idempotent and happens exactly once before `Wait`.
3. Write errors are attributed separately from read errors (ENOSPC must not log as a CDN hiccup).
4. `Convert` derives its output by trimming the input extension, not by string-replacing
   `_flv.mp4`: measured failure otherwise is `FFmpeg cannot edit existing files in-place`.
5. Per-connection restart: one muxer per stream connection, segments `<base>.NNN.ts`; an abandoned
   candidate's segments are removed.
