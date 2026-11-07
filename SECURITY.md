# Security policy

TrailSource connects to OPC UA servers, checks their certificates and writes
evidence records. A flaw in certificate or trust-list handling, in the
handling of credentials, or in record integrity is handled privately first.

## Reporting a vulnerability

Please do not report a suspected vulnerability in a public issue, pull
request or discussion.

Report it privately through GitHub instead: open the **Security** tab of this
repository and choose **Report a vulnerability**, or go straight to
<https://github.com/RainerGewalt/trailsource/security/advisories/new>. Only you
and the maintainer can see the report and the conversation about it.

Include what you can safely share: the affected version or commit, how to
reproduce it, and the impact as you understand it. Never include real
credentials, private keys or production data.

TrailSource only reads from OPC UA servers. Do not attach server
certificates with private keys, user credentials or data from a real plant
to a report.

## What happens after you report

- Reports are handled confidentially, and nothing from a report is copied
  into a public issue. A confirmed vulnerability stays confidential. Once it
  has been fixed, a coordinated disclosure, for example as a GitHub security
  advisory, may follow, crediting you if you want that. Neither a fix nor a
  publication date is promised.
- Reports are read and answered on a best-effort basis by a single
  maintainer. There is no guaranteed response or fix time, and none is
  implied by this document.
- A report that includes a reproduction is acted on before one that does not.
- If a fix is made, it ships in a regular release. There is no backport to
  older releases.
