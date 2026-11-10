# Local dashboard design

This document describes the embedded dashboard only. It does not replace the existing public installer site's visual identity.

The dashboard is a quiet inventory desk: warm paper, dark ink, a narrow green accent, and copper for unresolved changes. It should make a scattered setup feel inspectable without disguising uncertainty as a reassuring total.

Typography uses the platform sans for controls and data labels, Georgia for the opening sentence, and a monospace face only for paths, commands, and diffs. All fonts are available locally. The build requires no external assets.

The composition is an open ledger with a compact masthead, a clear machine identity, one check summary, and a full-width skill list. It avoids repeated metric cards. Skill selection opens an inspection panel; the central interaction is the distinction between accepted, installed, and current tool contents.

The page's controls are manual: check, filter, search, select, and copy an explicit CLI command. Refresh never applies changes. Loading, empty, partial, and failed checks remain visibly distinct.

Desktop retains useful source and owner columns. Narrow layouts prioritise the skill name and state, with full details available in the inspection panel. Reduced motion disables the panel's brief reveal. All report data is inserted as text, never interpreted as HTML.
