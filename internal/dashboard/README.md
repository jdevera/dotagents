# Local dashboard

Mode: Operate. The user checks an inventory, identifies untracked or changed
items, and inspects the accepted/installed/tool comparison before choosing a
CLI action.

`Handler(func() (any, error))` serves embedded assets and a read-only JSON report
at `/api/report`. The caller binds to loopback and validates Host headers.
There are no CDN requests, browser dependencies, or background refresh timers.

The first viewport establishes the current machine and its calculated groups,
then prioritises items that need attention. The signature interaction is a
skill inspection panel with three clearly labelled states and actual text
diffs. Commands are copied, not executed from the page.

Report fields are defined by the parent application. The UI tolerates absent
optional fields but never treats a failed tool comparison as a match. MCP
configuration must arrive already redacted; the page does not receive secrets.
