# Local dashboard design

This document describes the embedded dashboard only. It does not replace the existing public installer site's visual identity.

The dashboard is a compact working inventory. The user rejected the original sepia palette, generous spacing, and small type. Use neutral gray (#f7f8fa), white (#ffffff), dark text (#1b2430), and clear secondary text (#515e6d). Green (#187044), amber (#965000), and red (#b42318) communicate states; blue (#2463cc) marks keyboard focus.

Typography uses the platform sans throughout, with monospace only for paths, commands, and diffs. Body and controls are 16px, skill names 17px, secondary labels 14–15px, and the compact title 28px (24px on mobile). Do not shrink inventory text on mobile. All fonts are local; no external assets.

The composition brings the inventory into the first viewport: a 64px masthead, a compact 20px-padded introduction, a short check summary, and a full-width skill list. Rows use 8px vertical padding and a 62px minimum height. It avoids repeated metric cards. Skill selection opens an inspection panel; the central interaction is the distinction between accepted, installed, and current tool contents.

The page's controls are manual: check, filter, search, select, and copy an explicit CLI command. Refresh never applies changes. Loading, empty, partial, and failed checks remain visibly distinct.

Desktop retains useful source and owner columns. Narrow layouts prioritise the skill name and state, with full details available in the inspection panel. Reduced motion disables the panel's brief reveal. All report data is inserted as text, never interpreted as HTML.

Small stroke icons identify navigation sections and ownership types. Always pair them with text; use the existing inline SVG helper, with no icon font or external dependency.
