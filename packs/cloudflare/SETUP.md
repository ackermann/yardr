# Setting up Cloudflare

The pack wires Cloudflare into the yard as the declared source
`cloudflare`: the user's own account read as wrangler reads it —
accounts, zones, Workers scripts, Email Routing rules and destination
addresses. Every call reads; this pack writes nothing to Cloudflare.
Worker upload and Email Routing writes are a later pack's work.

1. **The API token, in Cloudflare.** My Profile → API Tokens →
   Create Token → Create Custom Token (or the "Edit Cloudflare
   Workers" template, trimmed to this list), with exactly these
   minimum scopes, on one account and one zone, plus the user-scoped
   permission needed to read the token's own policy:
   - **API Tokens Read** (User; needed for GET
     `/user/tokens/<token_id>` to report this token's scopes)
   - **Workers Scripts Edit** (Account)
   - **Email Routing Addresses Read** (Account)
   - **Email Routing Rules Edit** (Zone)
   - **Zone Read** (Zone)

   Account Resources: the one account. Zone Resources: the one zone.
   API Tokens Read is a User permission; it is not an account-wide or
   zone-wide grant.
   The yard reads Workers scripts and destination addresses under the
   account, Email Routing rules under the zone, and zones under the
   Zone Read permission. What the token may do is read back after
   setup, from the token itself (step 4): a scope it lacks shows
   there, beside this list. Copy the
   token once: Cloudflare shows it once.
2. **The setup card.** One card carries the pack's whole setup: the
   pack, the token and the grants. Have a crew member ask for it,
   and approve it, in footplate or in `yardr chat` (`ctrl+o`); a
   person sets up the packs they want, one card each:
   ```sh
   yardr bead ask <bead> "Set up Cloudflare for the yardmaster?" --setup cloudflare --crew yardmaster --recommend 1 --why "reading is the job; writes are a later pack" --context "The yardmaster reads the account, its zones, Workers scripts and Email Routing as wrangler reads them."
   ```
   The card shows the declaration whole, the token to type unseen,
   and one option per preset. Approving it applies the pack, keeps
   the token as the source's secret in
   `~/.yardr/sources/cloudflare.env` (mode 0600, named in the
   answer, never the value), and grants the preset picked. The
   token is in no call's arguments, then or later.
   By hand it is the operator's to type, starting with the pack:
   ```sh
   yardr pack apply cloudflare
   # applied pack cloudflare to the yard: 1 created, 0 changed, 0 unchanged
   # source cloudflare is declared: 7 calls under https://api.cloudflare.com/client/v4 (yardr source show cloudflare)
   yardr source key cloudflare           # CLOUDFLARE_API_TOKEN, typed unseen
   ```
3. **The grants.** A pack of kind source asks none at import, so
   what a crew member may call is picked on the setup card as one
   preset. The pack's preset, as `yardr pack show cloudflare`
   prints it numbered:
   1. `read`: every read call — the token verified and read with
      its scopes, accounts and zones listed, Workers scripts,
      Email Routing rules and addresses listed — and it covers
      any read call the source is later given; writes nothing.

   By hand it is the operator's to type:
   ```sh
   yardr source grant cloudflare read --crew yardmaster
   ```
4. **Check it, and pick the account and zone.** The check verifies
   the stored token; the reads then name what the token can read:
   ```sh
   yardr source check cloudflare
   # cloudflare: credential: CLOUDFLARE_API_TOKEN is set in ~/.yardr/sources/cloudflare.env
   # cloudflare: check call verify (GET /user/tokens/verify): 200 in …ms, … bytes
   # cloudflare: success: true
   # cloudflare: result: {"id":"<token id>","status":"active"}
   yardr call cloudflare accounts
   yardr call cloudflare zones
   yardr call cloudflare token token_id=<the id verify showed>
   ```
   `token` answers the token's policies: the scopes Cloudflare
   reports for it, to set beside step 1's five. A scope the token
   lacks is the one of the five its policies do not name. Then, with
   the account and zone picked from the lists:
   ```sh
   yardr call cloudflare workers account=<account id>
   yardr call cloudflare email_rules zone=<zone id>
   yardr call cloudflare email_addresses account=<account id>
   ```
   Each answers Cloudflare's envelope after its selection:
   `success`, the `result` list, and `result_info`. A list the
   token may not read answers 403 there, with the call named;
   remake the token with the missing scope (step 1) and give it
   again (step 2).

What is not there, on purpose: every write (no Worker upload, no
secret, no Email Routing rule made or changed); other Cloudflare
products (DNS, R2, Pages); and no credential but the one token.
