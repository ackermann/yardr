# Security policy

## Reporting a vulnerability

Report a vulnerability through GitHub's private vulnerability reporting. Open
<https://github.com/yardr-dev/yardr/security/advisories/new>, or go to the
repository's **Security** tab and choose **Report a vulnerability**. Only you
and the repository's maintainers can see what you send there.

Do not open a public issue or pull request about a vulnerability, and do not
post it anywhere else, until an advisory is published or we have agreed a date
with you. Anyone can read a public report, including people who would use it
before there is a fix.

A report is quickest to act on when it says:

- what the problem is and what it lets someone do;
- the version, from `yardr version` or the release tag, and how the yard runs,
  for example on a laptop, on a server, behind the edge or with paired devices;
- how to see it for yourself, in steps or a short script, on a yard you run.

We read every report and answer it in the advisory. The answer says whether we
take it as a vulnerability and what happens next. When the advisory is
published we credit you, unless you ask us not to.

## In scope

yardr as built and released from this repository:

- the `yardr` binary and what it runs: the server and its unix socket API, the
  listener that takes webhooks, peer messages and device calls, the web view,
  the Bead Protocol server, and the yard's end of the link to the edge;
- the packs and harness files built into the binary, and the scripts and tools
  under `contrib/`;
- the release archives, with their checksums and signatures;
- the documentation, where following it as written gives someone access they
  should not have.

We most want to hear about anything that lets someone do one of these without
the access the documentation gives them:

- read or change what a yard keeps, credentials included;
- run code on a yard, or call a service as the yard;
- pass themselves off as a person, a service or another yard.

Other problems in these parts are in scope too. If you are not sure whether
yours is, report it.

## Out of scope

- Software yardr works with but does not ship: herdr, the agent harnesses, git,
  and services such as GitHub, Linear and Slack. Report those to their own
  projects. If the way yardr uses one is the cause, report it here too.
- A yard set up against its documentation, for example one opened to a network
  without the protection the documentation calls for.
- Scanner output with no demonstration that the problem can be used.
- Social engineering, and physical access to a machine.

## Testing

Test against a yard you run yourself. Do not test against anyone else's yard.

The edge and the project's website are not built from this repository, and you
should not test against them either. If you have reason to think one has a
problem, report what you saw and why here, and say which it is.
