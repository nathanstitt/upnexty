# Plan: fetch events from the Google Calendar API

## Why

The board currently fetches each calendar's secret-address iCal export
(`calendar.google.com/calendar/ical/<email>/<key>/basic.ics`) and expands it
locally. Measured 2026-09-14 against the real feeds:

| | measured |
|---|---|
| Feed size | Personal 7.0MB + Rice 8.9MB = **15.9MB** |
| Events in the feeds | **5,091** (744 carrying RRULE, 674 EXDATE lines) |
| Events actually displayed | **43** (7-day window) |
| Parse cost | **241MB total allocation, 84MB live heap**, 1.9s on an M4 Pro |
| Peak RSS on the board | **91–97MB** against ~430MB usable |
| Fetch interval | every 10 minutes, forever |

So ~99% of the work is discarded. `events.list` with `singleEvents=true` and a
`timeMin`/`timeMax` window returns the ~43 instances directly, server-expanded.

Two further facts, both verified on hardware rather than assumed:

- **The iCal export sends no validator.** No `ETag`, no `Last-Modified`, no
  `Content-Length`, and `cache-control: no-cache, no-store, must-revalidate`.
  Conditional requests are impossible on that endpoint. The API does support
  ETags and `syncToken`; the docs that describe this cover the API, not the
  export.
- **The export IS served gzipped** — 782KB on the wire for the 7.0MB Personal
  feed, and Go's default transport already requests and decompresses it. The
  wire cost was never 16MB; the 16MB lands in memory and the parser.

## Step 0 results (verified 2026-09-14)

Device flow, the token exchange, and a real `events.list` were all run against
the live account before any code was written.

- **Device flow works for `calendar.readonly`.** `POST /device/code` returns
  200 with a `user_code`; the code exchange returns a token with the right
  scope. The assumption the design rests on holds.
- **One token covers both board calendars.** `nas@stitt.org`'s
  `users/me/calendarList` lists `ns51@rice.edu` with `accessRole: owner`, so a
  single authorization reads both. The multi-account worry — a token authorizes
  one account, and the board reads two — does not apply here. It would apply to
  any calendar the signed-in account cannot see, so check `calendarList` before
  assuming a new source is reachable.
- **The measurement:**

  | | iCal export | API, 7-day window |
  |---|---|---|
  | Bytes | 15,959,492 | **95,661** |
  | Events | 5,091 | **44** |
  | Recurring | 744 RRULEs, expanded locally | **41 of 44 pre-expanded** |

  167× fewer bytes, 116× fewer events, and the 44 returned match the 43 the
  iCal path renders — nothing is silently dropped, which is the failure the
  iCal date parameters showed.

- **`singleEvents=true` does expand recurrences.** 41 of 44 items carry
  `recurringEventId` and concrete start/end times. The local `icalRecur` /
  `EXDATE` / `RECURRENCE-ID` machinery is not needed for these sources.

### Refresh tokens expire in 7 days while the app is in testing

The token exchange returned `refresh_token_expires_in: 604799`. Google expires
refresh tokens after a week for apps in **testing** status, which on a
wall-mounted panel means the calendar dies every 7 days until someone
re-authorises.

**Publish the consent screen** (OAuth consent screen → Publish app) before
building on this. For a single-user app that is a status change, not a review;
the unverified-app warning at consent remains, which is fine for a board only
its owner sets up. Re-check `refresh_token_expires_in` after publishing — if it
is still present, the design needs a re-authorisation path on the panel rather
than a one-time setup.

## What this does not claim to fix

The lockups (see CLAUDE.md). Test 3 implicated external fetching, but the
mechanism is unknown, and the gzip finding above invalidated the "16MB over
TLS" magnitude that theory rested on. This plan is justified by the memory and
CPU numbers alone. If it also stops the lockups, that is a bonus to verify, not
a promise.

## Do not use the iCal export's date parameters

The GData-era query parameters still respond on the `.ics` endpoint, and the
size numbers look spectacular. **They are unusable.** Measured 2026-09-14
against the Personal feed:

| Request | Decompressed | VEVENTs |
|---|---|---|
| plain | 6,989,145 | 2,290 |
| `?futureevents=true` | 2,916 | 4 |
| `?start-min=…&start-max=…` | 1,869 | 2 |

A 2,400× reduction — and wrong. Dumping the events shows what comes back:

```
=== start-min+max ===
  20240516T083000  [RRULE] Exercise class
  20260408T110000  [RRULE] JP office hours
```

Only RRULE **masters**, with their original `DTSTART` years in the past, and
**every non-recurring event dropped**. So the response is small because it is
missing almost everything: one-off meetings disappear, and recurring ones still
need local expansion, which was the expensive part.

It returns `200 OK` with a well-formed calendar, so nothing fails — the panel
would simply show a nearly empty day. That is the same silent-failure shape as
the 8MB truncation bug, and it is why this was tested rather than adopted on
the size numbers.

`?singleevents=true` and `?max-results=N` are ignored outright (794KB and
788KB against a 787KB baseline).

**And the behaviour is not even consistent between feeds.** The same
`start-min`/`start-max` request, same moment:

| Feed | plain | filtered |
|---|---|---|
| Personal | 6,760,568 B / 2,290 events | 1,794 B / **2 events** |
| Rice | 8,644,007 B / 2,801 events | 98,131 B / **47 events** |

Rice really does filter — 47 events, non-recurring ones included, close to the
36 the API returns for it. Personal returns 2 RRULE masters and drops
everything else. Nothing distinguishes the two requests but the calendar.

Even where it filters, the events come back as **masters with their original
`DTSTART`** (`20210824`, `20250505`, `20260413`), not as occurrences, so the
local RRULE expansion — the expensive part — is still required.

So: inconsistent between feeds, silently wrong on one of them, and does not
remove the work it appears to remove. Not a shortcut.

## Scope

**In:** a second calendar backend, selected per source; OAuth device flow;
token storage and refresh; portal support for connecting an account; keeping
the iCal path for non-Google feeds.

**Out:** removing the iCal parser (other feeds still need it); `syncToken`
incremental sync (a follow-up once the basic path is proven); changing the
render or model layers, which already consume concrete instances.

## Design

### Where it plugs in

`calendar.Fetch(ctx, src, now, daysAhead, loc) ([]Event, error)` is already the
only entry point `cmd/dashboard` uses, and `model.BuildAgenda` works on the
concrete `[]Event` it returns. That is the seam: add a second implementation
behind the same signature, chosen by the source's kind. Nothing above
`calendar` changes.

### Config

`CalendarSource` grows a kind, a calendar id, and — importantly — the account
whose token reads it:

```go
type CalendarSource struct {
    Name    string `json:"name"`
    Color   string `json:"color"`
    URL     string `json:"url"`                // iCal sources only
    Kind    string `json:"kind,omitempty"`     // "" or "ical" | "google"
    CalID   string `json:"cal_id,omitempty"`   // google: calendar id, e.g. an email
    Account string `json:"account,omitempty"`  // google: which token reads it
}
```

Empty `Kind` means `ical`, so every existing config keeps working untouched.

**One token per account, not one token for the board.** A Google OAuth token
authorises a single account. Step 0 found that `nas@stitt.org`'s token also
reads `ns51@rice.edu` (it is listed in that account's `calendarList` with
`accessRole: owner`), so *this* board needs only one — but that is a property
of how those two accounts are shared, not a general rule. A calendar belonging
to an account the signed-in user cannot see needs its own authorisation, and a
design with a single global token has nowhere to put it.

So tokens are stored per account:

```
/root/google-tokens/<account>.json     mode 0600, dir 0700
```

`CalendarSource.Account` names the file. A source with an empty `Account` on a
board with exactly one token uses it, which keeps the common single-account
setup free of ceremony.

Tokens live outside `config.json` because the portal serves `config.json`-derived
state and a refresh token must never reach a page. The **client secret** is
separate again (`/root/google-client.json`, or compiled in): it identifies the
app, not the user, and is shared by every account.

Practical consequences the implementation must handle:

- **Refresh is per account.** One account's expired grant must not stop another
  account's calendars from updating — the same isolation `SetEvents` needs
  (see below), one level down.
- **The portal lists accounts, not just calendars.** "Connect Google Calendar"
  can be run more than once, producing several entries; each needs its own
  disconnect.
- **Discovery is per account.** After authorising, call `calendarList` for that
  account and let the user pick which of its calendars to show — that is how
  `CalID` gets filled in without the user typing calendar ids.
- **Deleting an account's token** must clear or disable the sources that
  reference it, rather than leaving sources pointing at a missing file.

### Hand-rolled client, not `google.golang.org/api`

The board has **66MB free on a 193MB rootfs** and the binary is already 17MB.
The official client pulls in a large dependency tree for two endpoints. A
hand-written client needs: a token struct, a refresh call, and one
`events.list` GET with JSON decoding — a few hundred lines against
`net/http` and `encoding/json`, both already linked.

`golang.org/x/oauth2` is a reasonable middle ground if the refresh logic proves
fiddly; decide after step 3, not before.

### OAuth on a device with no browser

Device flow (RFC 8628). The board has no browser and the panel is the only
output, so:

1. Portal shows "Connect Google Calendar"; POST starts the flow.
2. Board calls the device-code endpoint, gets `user_code` + `verification_url`.
3. **The panel displays the code** — it already renders arbitrary text and
   already shows the setup/AP credentials, so this is the same mechanism.
4. User visits the URL on a phone, enters the code, grants
   `calendar.readonly`.
5. Board polls the token endpoint, stores the refresh token, redraws.

**This is the WiFi-association flow again, and should reuse its machinery.**
The browser that submits the form cannot see the outcome — the code appears on
the panel, seconds later, while the board polls Google. That is exactly the
problem `portal.Connecting()` and `model.SetupHint` already solve:

- `SetupHint` exists to put setup instructions on the panel (it currently
  carries `APName`, `Password`, `URL`) and already has a `Connecting` field for
  reporting an in-flight attempt instead of static instructions. The device
  code and verification URL belong there as new fields, rendered by the same
  template branch.
- `portal.Connecting()` is the polling accessor the dashboard already calls
  each render. A `portal.PairingCode()` alongside it follows the established
  shape: atomic value, set when the flow starts, cleared when it ends.

Do not build a second mechanism for this.

Access tokens last ~1h; the refresh token is long-lived. Refresh on 401 or when
expiry is within a minute.

**Refresh failure must be loud.** A rejected refresh token (revoked access,
password change, 6-month inactivity) means the calendar is dead until the user
re-authorises, and the existing plumbing carries that: `Store.SetEvents`
records `errs`, `Store.Snapshot` returns them, and `model.Build` sets
`ViewModel.Stale` from `len(errs) > 0` — which the panel already renders as
"this may be older than it looks". Route the reconnect-needed state through
`errs` so it lights that up, and put the re-authorise instruction in
`SetupHint` so the panel says what to do about it.

**Back off on failure.** `retryDelay` is 30s, which is fine for a transient
network error and wrong for a revoked token — it would poll Google's token
endpoint 2,880 times a day against a credential that will never work again.
Distinguish the two: retry transport errors at `retryDelay`, but treat an
`invalid_grant` as terminal until the user reconnects, retrying at most hourly.
Google publishes per-project quotas; a tight loop across two calendars is the
one way this design could plausibly hit them.

**The clock matters here.** The board has no RTC and boots at 1970 until
`S99wlan0` runs `rdate`. OAuth expiry arithmetic and TLS certificate validation
both depend on the clock, so the Google path must not be attempted before the
time is set. `S99zdashboard` already sorts after `S99wlan0`, but the dashboard
retries on its own schedule — so gate the first Google fetch on a sane clock
(year > 2020) and let the existing retry handle the rest.

### The request

```
GET https://www.googleapis.com/calendar/v3/calendars/{calID}/events
    ?timeMin=<now-24h, RFC3339>
    &timeMax=<now+daysAhead, RFC3339>
    &singleEvents=true
    &orderBy=startTime
    &maxResults=250
Authorization: Bearer <access token>
```

- `singleEvents=true` is what makes the server expand recurrences and return
  instances; it is also required for `orderBy=startTime`.
- `timeMin` reaches back 24h to match the existing `pastWindow`, which is what
  keeps the "last thing that happened" card populated.
- Cancelled occurrences are simply absent; moved ones arrive with their
  overridden times. This deletes the local `icalRecur` / `EXDATE` /
  `RECURRENCE-ID` handling for these sources.
- **Paginate.** `nextPageToken` must be followed rather than assumed absent; a
  denser week or a larger `days_ahead` can exceed one page.

### Mapping to `Event`

| API field | `Event` |
|---|---|
| `summary` | `Title` |
| `start.dateTime` / `start.date` | `Start`, `AllDay` when `date` is set |
| `end.dateTime` / `end.date` | `End` |
| `id` | `UID` — used by the mute feature |
| `location`, `description` | `Location`, `Description` |
| `attendees[].self && responseStatus` | `Status` — drives ghosting and the declined drop |

`Status` is the one that needs care: the iCal path derives the owner from the
URL and matches `ATTENDEE`; the API marks the owner's attendee record with
`self: true`, which is more reliable. Declined events must still be dropped
entirely, matching current behaviour.

### Mutes across the switch

`Event.Key()` is `UID + "@" + Start.UTC()`. The API's instance `id` is not the
iCal `UID`, so every existing mute stops matching the moment a source switches
kind — the muted events quietly come back.

`MutedEvent` already persists `Key`, **`Title`**, `Start` and `End`, so the
information needed to re-key is on disk. Two options, decide in step 4:

- **Re-key on load** (preferred): when a muted entry's `Key` does not match any
  event but its `Title`+`Start` do, rewrite the stored `Key` to the new one.
  Self-healing, runs once, no user action.
- **Match on `Title`+`Start`** as a fallback inside `IsMuted`. Simpler, but
  weakens identity for every feed, not just migrated ones.

`Key()` already degrades to `"title:" + Title` for feeds without UIDs, so a
title-based identity is an established fallback in this design rather than a
new concept.

Do **not** silently drop the mutes: unhiding an event the user deliberately hid
is a visible regression with no error attached.

### Window and trimming must not drift

The API request replaces where events come from, not which ones are shown.
`Agenda.DaysAhead` (7) sets `timeMax`, `pastWindow` (24h) sets `timeMin`, and
`MaxEvents` (40) plus `calendar.TrimPast(..., KeepPast)` must keep operating on
the merged set exactly as now. A Google source returning pre-trimmed data must
still pass through the same `TrimPast`/`MaxEvents` path, or a mixed config
trims two ways and the row changes shape depending on which feed an event came
from.

`orderBy=startTime` is per-request; `fetchCalendars` already re-sorts the merge,
which stays necessary with two sources.

### Fix `SetEvents` first — mixed configs make its bug dangerous

`fetchCalendars` merges every feed and calls `store.SetEvents(all, errs)`.
`SetEvents` then does:

```go
if len(errs) == 0 {
    s.events = evs
}
```

So **one failing feed discards the events from every feed that succeeded**, and
the panel keeps serving the last fully-clean fetch until every source succeeds
at once. Its own comment says "evs is expected to be nil in that case" — the
only caller never passes nil.

Today that is latent. This plan makes it live: the normal migration state is
one Google source plus one iCal source, and the Google one has a brand new way
to fail (expired token, quota, revoked grant) that can persist for days. A
board in that state would freeze its whole agenda on stale data, including the
calendar that is working perfectly.

**Fix it as step 0**, before any of this: keep last-good data per feed rather
than globally, so a failing source preserves only its own events and the
healthy sources update normally. The failing source still contributes its error
to `errs`, so `Stale` still lights up.

This is worth doing whether or not the rest of the plan proceeds.

## Steps

**Step 0 — verify device flow is available for this scope.** Before writing
anything: create the Cloud project, make a "TV and Limited Input device" OAuth
client, and `curl` the device-code endpoint for
`https://www.googleapis.com/auth/calendar.readonly`. Google restricts which
scopes that client type may request, and this plan is built on it working. One
request answers it; discovering otherwise at step 5 wastes the whole build.

The fallback if it is refused: run the authorisation-code flow against
`http://127.0.0.1` on a laptop once, and paste the resulting refresh token into
the portal. Worse UX, same runtime path from step 3 onward.

**Step 0b — fix `SetEvents`** (see above). Independent of everything else here.

1. **Add `Kind`/`CalID` to `CalendarSource`**, defaulting empty to `ical`.
   Config round-trip tests; confirm existing configs load unchanged.
2. **Split `calendar.Fetch`** into `fetchICal` and a `Fetch` that dispatches on
   kind. Pure refactor, no behaviour change, existing tests stay green.
3. **Token store, keyed by account**: `/root/google-tokens/<account>.json` at
   0600 in a 0700 directory. Load, save, refresh near expiry, and distinguish
   a transient failure from `invalid_grant`. Unit tests against an `httptest`
   server covering the backoff, **and two accounts where one's grant is
   revoked and the other keeps working** — the isolation is the point.
4. **`fetchGoogle`**: resolve the source's `Account` to a token, build the URL,
   decode, paginate, map to `Event`, re-key mutes. Test against recorded JSON
   fixtures in `testdata/` — a recurring series, a moved instance, a cancelled
   one, an all-day, a declined invite, and **a two-page response**, since
   pagination is the easiest thing to get wrong and never notice with a light
   calendar.
5. **Device flow + portal**: "Connect Google Calendar" (repeatable, one run per
   account), code via `SetupHint` on the panel, polling through the
   `Connecting()` pattern. After a successful authorisation, call
   `calendarList` for that account and let the user tick which calendars to
   show — that fills in `CalID` and `Account` without anyone typing a calendar
   id. Disconnect is **per account** and must also deal with the sources
   pointing at it.
6. **Verify on hardware**: RSS/HWM before and after via `/root/health.log`, and
   **the same events as the iCal path on the same day** — run both side by side
   for a day before removing anything. A silent mismatch is the failure mode
   this whole plan exists to avoid, and today already produced two of them (the
   8MB truncation, and the iCal date parameters).

Steps 1–4 are testable on the host with no board and no Google account. Step 0
and step 5 need real credentials.

## Risks

- **A Google Cloud project is required** — client ID/secret, consent screen,
  `calendar.readonly` scope. That is account setup the current "paste a URL"
  flow avoids entirely, and it is the main cost of this change.
- **Device flow may not be available for `calendar.readonly`.** This is an
  assumption, not a verified fact — Google limits which scopes the "limited
  input device" client type may request, and I have not tested it. Step 0
  exists to settle it before any code is written. Treating this as known is
  precisely the mistake that made the iCal date parameters look like a fix.
- **The board's clock** breaks OAuth if the Google path runs before `rdate`.
  Gated in step 4; verify from a cold boot, not just a restart.
- **Mutes need re-keying** across the switch (above). Solvable from data
  already on disk, but it has to be done deliberately — the failure mode is
  hidden events silently reappearing.
- **Quota and backoff.** A revoked token retried every 30s is 2,880 requests a
  day per calendar against a credential that cannot recover. Handled in step 3.
- **Binary size** — check after step 4 and reconsider the official client if a
  hand-rolled one is growing awkward.

## Expected result

Per fetch, per calendar: **~43 events instead of 5,091**, tens of KB of JSON
instead of 16MB decompressed, and no local recurrence expansion. The 91–97MB
render peak should fall to roughly the no-calendar baseline of ~50MB.
