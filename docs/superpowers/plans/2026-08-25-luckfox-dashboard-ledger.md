# SDD ledger — plan: docs/superpowers/plans/2026-08-25-luckfox-dashboard.md
Task 1: complete (commits 836c40a..c676168, review clean)
Task 1: minor (deferred): build.sh — `-C <dir> dashboard` combo fires the dashboard branch against a foreign MODULE_DIR; guard with [ "$MODULE_DIR" = "$REPO_ROOT" ] if worth hardening
Task 2: complete (commits c676168..b2a71a6, review clean)
Task 2: minor (deferred): nested anonymous structs not independently referable types; error wrap omits explicit path
Task 3: implemented (commit 100c6a8) — review: spec OK, port byte-faithful across all 19 fns (mechanical diff), 2 Important findings
Task 3: fix round 1/5 dispatched — (a) Parse returns unsorted events (Pi sort was at main.go:1173, outside ported range — plan defect); (b) zero coverage for Status/PARTSTAT, 1h DTEND fallback, icalUnescape
Task 3: minor (deferred): owner := ownerEmail pass-through alias could be inlined
Task 3: observations (pre-existing in source, NOT port defects): startOfWeek ignores WKST; all-day/no-TZID parse falls back to time.Local; nthWeekdayOfMonth treats BYDAY ordinal 0 as 1st
Task 3: gap for later fixture work: icalOccKey's t.UTC() normalization is load-bearing but uncovered (all fixtures use Z); MONTHLY/YEARLY and INTERVAL>1 WEEKLY untested
Task 3: fix round 1/5 (4 addressed, 0 open; commits 100c6a8..7d35340) — sort added, 5 new tests close Status/DTEND/unescape gaps
Task 3: complete (commits b2a71a6..7d35340, review clean) — 13/13 tests
Task 4: implemented (commit 0833823, live Open-Meteo fixture 168h/7d) — review: spec OK, 3 Important findings
Task 4: fix round 1/5 dispatched — (a) silent 0-fill on short sibling arrays; (b) unguarded zero time from parseLocal; (c) tests can't catch index transposition
Task 4: fix round 1/5 (5 addressed, 0 open; commits 0833823..de0f09b) — 7 sibling arrays validated, 3 timestamp sites guarded, spot checks kill off-by-one mutant (verified)
Task 4: complete (commits 7d35340..de0f09b, review clean) — 4/4 tests
Task 4: minor (deferred): TestParseOpenMeteoRejectsMalformedTimestamp asserts err != nil but not error content
Task 5: implemented (commit 1a5a58f) — review: spec OK, linearity invariant mutation-verified, 3 Important findings
Task 5: fix round 1/5 dispatched — (a) MaxSpan clamp drops the event FitEvents grew for [RULING: MaxSpan is a hard bound, FitEvents best-effort within it]; (b) MinSpan>MaxSpan unguarded; (c) undocumented sorted-input contract (matters at Task 7 multi-feed merge)
Task 5: fix round 1/5 (3 addressed, 2 open; commits 1a5a58f..7e21005) — F1/F4/F5 addressed; F2 NOT addressed (MaxSpan still exceeded when no event qualifies — reproduced independently: 6h span vs 2h bound); F3 doc OK but test vacuous
Task 5: fix round 2/5 dispatched — unconditional final clamp before Window construction; real test for no-qualifying-events path; de-vacuum F3 test; tighten F1 test to assert Blocks() exclusion
Task 5: fix round 2/5 (3 addressed, 0 open; commits 7e21005..3dde8d5) — single unconditional MaxSpan clamp, 3 empty-loop regression tests, vacuous test deleted, F1 test asserts Blocks() exclusion
Task 5: complete (commits de0f09b..3dde8d5, review clean) — 16/16 tests
Task 6: implemented (commit 0ceea53) — review: spec OK, collision avoidance mutation-verified, 1 CRITICAL + 1 Important
Task 6: fix round 1/5 dispatched — CRITICAL: TrackWidth clamp pushes label left over the previous one and records rowEnd as if no collision (reproduced: two labels both claiming [900,1000)); dead `if !placed` fallback
Task 6: fix round 1/5 (1 addressed, 2 open; commits 0ceea53..e5e54cc) — Anchor OK; F1 only fixed for non-terminal rows (MaxRows=1 and crammed-edge cases still overlap, reproduced: 4 labels stacked at identical X=950); F2 !placed block still dead
Task 6: fix round 2/5 dispatched — run collision re-check on ALL rows incl. terminal; rowEnd must advance to true occupied edge (max), never move backwards; delete-or-justify !placed block
Task 6: fix round 2/5 (2 addressed, 1 new; commits e5e54cc..ae2af1e) — F1/F2 fixed+verified; NEW Important: push-right overflow unbounded (reproduced 9020px on 1540px track, 5.9x)
Task 6: fix round 3/5 dispatched — RULING (plan owner): a window with more events than can be labeled should emit FEWER labels, not overlap/stack/overflow. Adds LabelOpts.MaxOverflow; unplaceable labels are DROPPED.
Task 6: CONTRACT CHANGE for Tasks 7/9 — PlaceLabels no longer returns one Label per Block. Do not assume index alignment between Blocks and Labels.
Task 6: fix round 3/5 (1 addressed, 0 open; commits ae2af1e..47a618f) — MaxOverflow bound, unplaceable labels dropped; dense 50->7, mild 5->5 (verified)
Task 6: cleanup (commit 627f11a) — gofmt 3 files, removed dead `placed` var; behavior byte-identical (7/5 unchanged)
Task 6: complete (commits 3dde8d5..627f11a, review clean) — 28/28 tests
Task 7: implemented (commit 3e13564, 36/36) — review: spec OK, 3 Important findings (all inherited from plan's draft code)
Task 7: fix round 1/5 dispatched — (a) ViewModel had no Hourly => Task 9 couldn't render the chart [plan defect]; (b) Stale required BOTH sources to fail, hid partial failures; (c) UntilNext printed "0m" in the final minute
Task 7: CONTRACT for Task 9 — ViewModel becomes self-sufficient; Render(vm) takes ONLY the view model, not a separate *weather.Weather
Task 7: fix round 1/5 (4 addressed, 0 open; commits 3e13564..f259c13) — Hourly added, Stale=len(errs)>0, sub-minute reads "now"; boundary trace clean
Task 7: complete (commits 627f11a..503000c, review clean) — 42/42 tests + Stale doc moved to struct field
Task 8: implemented (commit ed5c822, 6/6, 11 icons authored) — review: spec OK, 1 Important + 3 Minor
Task 8: fix round 1/5 dispatched — Important: temp y-axis seeded from w.Hourly[0] BEFORE window filtering, so an out-of-window outlier flattens the curve (reproduced: same temps span y 68->12 without outlier vs 18->12 with; 56px of variation collapsed to 6px)
Task 8: fix round 1/5 (4 addressed, 0 open; commits ed5c822..1fe4935) — y-axis derived from filtered pts; verified identical y-coords with/without outlier, full chart height used
Task 8: complete (commits 503000c..1fe4935, review clean) — 8/8 chart tests, 11 SVG icons
Task 8: note (design tradeoff, not a defect): chart window is now strictly half-open [Start, End) — a point landing exactly on win.End is excluded rather than clamped
Task 9: implemented (commit 4ad5b83, 5/5) — contracts correct; implementer's browser check caught a NOW-tick/ribbon collision
Task 9: ENGINE GAP FOUND (doctaculous, not this repo) — no CSS custom property support. var(--x) silently resolves to nothing; the dark theme rendered black-on-white. Isolated: `.a{background:var(--bg)}` blank vs `.b{background:#0b0d12}` correct. No var()/custom-prop handling in pkg/css; absent from FEATURES.md.
Task 9: RULING (Nathan) — do NOT work around engine limitations. Stylesheet keeps var(); doctaculous will gain var() support. Fix round cancelled, style.css left as authored.
Task 9: verification note — CSS correctness must be checked by rasterizing through doctaculous, not by opening in a browser (browsers support var(), so a preview hides engine gaps).
Task 9: review (spec OK, no Critical; escaping traced clean, layout arithmetic verified 380+1540=1920 and bands fit 480) — 2 Important test-coverage gaps
Task 9: fix round 1/5 (3 addressed, 0 open; commit 2bb5f69) — stale flag asserted both directions, leader mark covered by real 40.7px drift from actual events (independently reproduced), dead-branch comment
Task 9: complete (commits 1fe4935..2bb5f69, review clean) — 8/8 view tests; stylesheet untouched, still 16 var() usages
Task 9: DELIVERABLE for doctaculous — docs/doctaculous-css-gaps.md (commit 9441d9e) lists 6 unimplemented CSS features with isolated repros
Task 10: implemented (commit ae07063) — go.sum created; E2E verified: golden HTML -> RenderHTML 1920x480 full-bleed -> Pack 3686400 bytes -> inverse rotation recovers upright image
Task 10: fix round 1/5 (2 addressed, 0 open; commit 66a19d5) — Pack precondition panic on aspect mismatch (was silently cropping), 90/180 branches kept + real pixel tests
Task 10: complete (commits 2bb5f69..66a19d5, review clean) — 8/8 fb tests
Task 10: ENGINE GAP #7 (doctaculous) — renderPage(_ context.Context) discards ctx and OpenHTMLBytes takes none, so a hung HTML render cannot be cancelled. Documented in docs/doctaculous-gaps.md; NOT worked around per ruling. Task 12 must not rely on ctx as a render timeout.
Task 11: complete (commits 66a19d5..bfc4e3b, review clean, NO fix round) — round trip independently verified: 4 corner blocks land exactly right, no mirror/shear. .gitignore exception needed because /tools/ was ignored wholesale.
Task 12: implemented (commit 75a6ae2) — race clean, --once verified end-to-end with live weather; review: spec NO (2 requirements not actually met), 1 Critical
Task 12: fix round 1/5 dispatched — CRITICAL: fetchLoop retry reads CONCATENATED errs, so a failing calendar pins the weather loop to 30s retries forever (120 req/hr vs 4); Important: change-detection skip is dead code (clock guarantees HTML differs — MY plan design error); Important: no recover(), one panic ends the kiosk
Task 12: fix round 1/5 (3 addressed, 0 open; commit 420ec94) — retry now per-source via fetcher's own ok bool (cross-talk structurally impossible), dead change-detection removed, recoverRender wraps full pipeline + loop continues
Task 12: complete (commits bfc4e3b..420ec94, review clean) — 8/8 cmd tests, -race clean
ALL NON-HARDWARE TASKS COMPLETE (1-12). Task 13 requires user approval — it deploys to the physical board.
FINAL WHOLE-BRANCH REVIEW (opus, 29 commits / 48 files / ~4.7k lines): verdict "needs work" — 3 Important cross-cutting bugs the per-task reviews structurally could not see
  (1) MaxEvents truncated BEFORE sorting concatenated feeds => a busy feed A could evict ALL of feed B incl. imminent events
  (2) calendar never received a location; 3x time.Local => on this board (TZ unset) all-day events parsed 5h off from the configured zone. REPRODUCED.
  (3) fetch error strings assembled, threaded through 3 layers, then discarded by an unreachable else-if
FINAL FIX WAVE (commits 9797d14, 2eb7e4f, 0cdc3cc) + tzid.ics fixture — re-review: ALL 4 ADDRESSED, verdict READY TO MERGE
  FIX 4 independently confirmed load-bearing (removing .UTC() makes the test fail); implementer's first fixture attempt did NOT catch it and they iterated until it did
ENGINE GAP #8 (doctaculous, commit b13eebc) — no overflow-wrap/word-break; long error URLs overflow. Documented, not worked around.
DEFERRED MINORS triaged by final review: all "ship it" except the two folded into the fix wave.
SPEC GAP (flagged, not implemented): past_temps.json history — spec said "worth keeping"; silently dropped in Task 4. Needs an explicit decision.
DECISIONS (Nathan): Task 13 (hardware) deferred — board disconnected, will run when reconnected. past_temps -> follow-up task, spec amended (a4ab79b) so it is tracked not silent.
MERGED to main as 9ed8f47 (--no-ff). feat/dashboard branch retained.
REMAINING: Task 13 (deploy + verify on hardware) — see the plan's Task 13 for the exact steps.
