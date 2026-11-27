# TODO: move the "login expired" alert out of the shared pgd code (work machine to fix)

Status: NOT started. The code stays as it is until this is done on the work macOS machine, where
the SSO login it checks actually exists. This file is a hand-off note, not a scheduled work item.

## The problem

The band's `⚠ pgd: login expired` alert is always on for any machine that has no SSO login, even
when the machine's AWS access works. Seen on the Linux host `monorepod`: its only AWS profile uses
static keys against an S3-compatible endpoint, there is no `~/.aws/sso/cache` directory, and the
alert shows permanently.

Cause: `readLogin` in `hooks/assume.ts` reads one fixed SSO token cache file
(`<AWS_SSO_CACHE_DIR, else ~/.aws/sso/cache>/<sha1 of the session name>.json`) and turns every read
failure, including "no such file", into the state `missing`. `loginNeedsAction` treats `missing`
as needing action, so `login-expired` in `hooks/alerts.ts` fires. The session name defaults to a
work-specific value when `ZR_SSO_SESSION` is unset, so the check is written for the work machine's
setup and is wrong anywhere else.

The check is specific to the work machine's SSO setup, so it MUST NOT live in the shared, generic
pgd code of this public repo (see "Public Repository" in the repo `CLAUDE.md`).

## What the work machine MUST do

1. Move the SSO login check (the session-name default, the cache-file read, `parseSsoLogin`,
   `sha1Hex`, the `login-expired` alert item and the modal's `login` row) out of
   `claude-marketplace/pgd` and into the work-specific repo (`phillipg-nix-ziprecruiter`), supplied
   as configuration or as a separate plugin.
2. Raise the alert through the contribution API that pgd already exposes, not through a built-in
   alert item: `$.pgd.put({ id: 'login-expired', surface: 'alert', priority: 50, text: 'login expired' })`
   and `$.pgd.remove({ id: 'login-expired' })` when the login is healthy
   (`hooks/pgd-noun.ts`; a put with the same `id` replaces the earlier one). The contributor owns
   the one-minute poll, so a machine without the contributor shows no login alert at all.
3. Remove the built-in item from `ALERT_ITEMS`, drop `login` from `AssumeValue`, and update
   `claude-marketplace/pgd/README.md` (the "Alert" and "Login state source" sections),
   `hooks/assume.test.ts` and `hooks/alerts.ts` in the same change.
4. Change the alert text so it says WHAT auth is missing. The check is about the AWS SSO login,
   not the Claude login, but `login expired` reads as Claude's own login. The text MUST name AWS
   (for example `AWS login expired`), and the modal's `login` row label SHOULD say AWS as well.
5. Keep the Assume role picker (role list, Apply, Clear) in shared code: it only reads
   `~/.aws/config` and sets environment variables, and works on every machine.

## Interim fix, if the move is not done soon

If the shared code has to be corrected before the move, the minimum is that "no cache file and no
`[sso-session]` section in the AWS config" MUST read as "not applicable", never as `missing`, so
only a cache file that exists and has expired raises the alert. The session name SHOULD come from
the `[sso-session NAME]` sections that `parseAwsConfig` currently skips, not from a hard-coded
default.

## A more direct check, and why it is not the recommendation

The mod API does offer `$.process.run(argv, init)` (no shell), exposed to the hooks as
`host.process.run`, so a contributor COULD run `aws sts get-caller-identity` and treat a non-zero
exit as "login expired". That is auth-method agnostic (static keys, SSO and assumed roles all
work), but:

- It makes a network call every poll, and the README records that an earlier design that ran `aws`
  fired on healthy sessions and could block for minutes when a lapsed login opened a browser.
  Any use MUST set a short timeout and MUST NOT run `aws sso login`.
- It cannot tell "expired and refreshable" from "expired for good": the next AWS call renews a
  refreshable token, whereas the cache-file read can tell them apart.
- A failure may be a network or endpoint problem, not a login problem, so the text would have to
  say less than "login expired".

So the work machine SHOULD keep the cache-file read inside its own contributor, and use the
`sts` call at most as a secondary confirmation.

## Done when

- On the Linux host, with no SSO configured, `⚠ pgd: login expired` never shows.
- On the work machine, the alert still shows for an expired, unrefreshable login and clears after
  `aws sso login`, now raised by the contributor.
- Nothing work-specific remains in `claude-marketplace/pgd`.
