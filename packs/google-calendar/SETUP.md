# Setting up Calendar

The pack wires Calendar into the yard as the declared source
`google-calendar`: events listed and read, the calendars listed, timed
events created and edited without inviting anyone. Each call goes out
with a
short-lived token minted from a gws login on this machine, so no new
copy of a secret is stored anywhere. The login, the account, the mint
and calling as a user are the four google packs' shared setup, said
once in `yardr docs google`.

1. **The gws login.** It needs the Calendar scope,
   `https://www.googleapis.com/auth/calendar`, else the calls come
   back 403. The shared login instructions in `yardr docs google`
   start from a machine with no gws, and show how to check and add
   the `calendar` service while keeping the other applied packs'
   scopes.
2. **The setup card.** One card carries the pack's whole setup: the
   pack, the account and the grants. Have a crew member ask for it,
   and approve it, in footplate or in `yardr chat` (`ctrl+o`); a
   person sets up the packs they want, one card each:
   ```sh
   yardr bead ask <bead> "Set up Calendar for the yardmaster?" --setup google-calendar --crew yardmaster --recommend 1 --why "reading is the job; creating can follow" --context "The yardmaster lists and reads calendar events to work beads."
   ```
   The card shows the declaration whole with its mint, the account
   to type or take (step 3), and one option per preset (step 5).
   By hand it is the operator's to type, step by step, starting
   with the pack:
   ```sh
   yardr pack apply google-calendar
   # applied pack google-calendar to the yard: 1 created, 0 changed, 0 unchanged
   # source google-calendar is declared: 5 calls under https://www.googleapis.com (yardr source show google-calendar)
   ```
3. **The account.** The yard's Google account for
   `google-calendar` is answered on the setup card, as described in
   `yardr docs google` (step 3): typed unseen on the first pack, then
   picked.
4. **The mint.** Nothing to type: the pack brings `google-mint`
   beside its declaration, and applying it (step 2) copies it for
   `google-calendar`, its sha256 on the card. What it needs and refuses,
   and how a mint of the operator's own replaces it, are in
   `yardr docs google`.
5. **The grants.** A pack of kind source asks none at import, so what
   a crew member may call is picked on the setup card as one preset.
   The pack's presets, as `yardr pack show google-calendar` prints
   them numbered:
   1. `read`: every read call — events and calendars listed and read —
      and it covers any read call the source is later given; changes
      nothing.
   2. `create`: every read call, and `create`: timed events on the
      yard's calendar; no invitations go out.
   3. `everything`: every read call, `create` and `edit`: events
      created, and edited changing only the fields it names; no
      invitations go out.
   A grantee who comes later gets one preset grant card:
   ```sh
   yardr bead ask <bead> "Let the reviewers read Calendar" --run "source grant google-calendar --preset read --group reviewers" --context "The reviewers list and read calendar events to check a bead's work; the card names each call it covers."
   ```
   By hand they are the operator's to type:
   ```sh
   yardr source grant google-calendar read --crew yardmaster
   yardr source grant google-calendar create --crew yardmaster
   yardr source grant google-calendar edit --crew yardmaster
   ```
   As ada, the same grants with `--as ada`, on cards addressed to
   ada and approved by ada alone: the shared card is in
   `yardr docs google` ("Calling as a user"), one card per grant, with
   `read`, `create` and `edit` from above. There is no typed way beside
   these: a grant `as` ada is ada's to give.
   `yardr source show google-calendar` lists each call with its kind
   and who may make it, plain and `as` each user.
6. **Check it.** The calendar source's check call lists the
   calendars, minted and live:
   ```sh
   yardr source check google-calendar
   # google-calendar: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted, good until 16:59
   # google-calendar: check call calendars (GET /calendar/v3/users/me/calendarList): 200 in 412ms, 1834 bytes
   ```
7. **Try it.** As the operator, who may make any call: the calendars,
   this week's events, one event read, an event created:
   ```sh
   yardr call google-calendar calendars
   yardr call google-calendar events calendarId=primary timeMin=2026-10-20T00:00:00Z timeMax=2026-10-27T00:00:00Z maxResults=10
   yardr call google-calendar event calendarId=primary eventId=<an id the listing showed>
   yardr call google-calendar create calendarId=primary summary="Lunch with Ada" start=2026-10-24T12:00:00+02:00 end=2026-10-24T13:00:00+02:00 location="Cafe Roma"
   ```
   An edit changes only what it names; what it leaves out stays:
   ```sh
   yardr call google-calendar edit calendarId=primary eventId=<an id> location="Cafe Novo"
   ```
   Every call names its calendar: `primary` is the account's own.
   Times are RFC 3339 with their zone, and the event created or
   edited invites no one: no invitation goes out of the yard.

Both write calls set `sendUpdates=none`. Calendar documents that as
no notifications and warns it can prevent syncing to external
calendars or lose events for some users. An edit to an event with
existing guests also sends them no change notice. Use these calls
only when that is intended ([events.patch](https://developers.google.com/workspace/calendar/api/v3/reference/events/patch),
[events.insert](https://developers.google.com/workspace/calendar/api/v3/reference/events/insert)).

Calling as a user — the yard reading another person's calendar with
their own credential — is the shared setup too: `yardr docs google`
has it.

What is not there, on purpose: inviting anyone (no attendees, and
sendUpdates is none); moving or deleting an event; all-day events
(a date without a time); sharing a calendar; paging past the first
page; and no call the pack does not list. The yard's source format
cannot express what is not listed, and the pack does not change
yardr.
