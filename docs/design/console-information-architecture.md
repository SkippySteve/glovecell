# Console information architecture

A short audit of where things live in the operator console (`web/`), what was
hard to find, and what the navigation work changed. Written for the console
redesign (theme, nav, control, attention and previews).

## Before

| Route | Page | Contents |
|---|---|---|
| `#/` | Home ("Sandboxes") | Web access panel (open or lock all), one table of sandbox-control sandboxes and console projects, removed projects |
| `#/new` | New sandbox | Create a console project from a repository |
| `#/sandboxes/<id>/<section>` | Sandbox workspace | Ten sections, own sidebar with every workspace and the current one's sections |
| `#/projects/<name>/<section>` | Project workspace | Same layout; Packages and Previews say "not available"; no Runs or Settings |
| `#/runs`, `#/runs/<id>` | Runs | Factory run list and one report |
| `#/orca` | Orca projects | Publish a repository into the Orca runtime |
| `/sandboxes/?workspace=…` | Classic page | sandbox-control's own UI (outside the app) |

The ten sections: Overview, Internet, LAN devices, Packages, Files (route
`downloads`), Previews & dev links, Credentials, History, Runs, Settings.

The top bar had only the brand (Home), Runs and Orca projects.

## What was buried

- **Sections were reachable only from inside a workspace.** To get to
  orca-pilot's Internet section you had to open Home, find the row, open the
  sandbox and pick the section. No search anywhere.
- **What waits on you** was a column on Home and badges in the workspace
  sidebar; there was no single place listing approvals, blocked requests or
  problems across sandboxes.
- **Activity** lived only in each workspace's History section. Home had no
  sense of what changed recently.
- **Projects vs sandboxes**: the two kinds share one table and one sidebar
  list, distinguished only by a small kind label; there was no list of just one
  kind.
- **Orca projects** (repositories in the Orca runtime) appear as a line under
  one Home row and as a top-bar link; they are easy to mistake for console
  projects.
- **Files** is routed as `downloads`, so a typed `#/…/files` landed on Overview.
- On phones the workspace sidebar was a `<select>` picker plus section pills,
  and Runs and Orca were only in the top bar.

## What was duplicated

- The workspace list existed twice: the Home table and the workspace sidebar.
- Breadcrumbs were hand-written per page (New sandbox, Orca projects, Runs),
  each starting at "Sandboxes", and the workspace pages had none.
- The "Classic page" link appeared both in the workspace sidebar and in the
  workspace header.
- Section summaries appear as Overview tiles, sidebar badges and header grant
  chips. This is deliberate (glance, navigate, act) and was kept.

## After

- **One app shell** (`web/src/shell/`): top bar with search and the theme
  switcher; a sidebar on desktop with Home, Needs attention, Sandboxes and
  Projects (each workspace listed, the current one expanded to its sections),
  Runs, Orca projects and New sandbox; a Menu button below 60rem.
- **Breadcrumbs** for every page from one function (`breadcrumbs()` in
  `router.ts`); the per-page ones were removed.
- **Ctrl/⌘-K palette** over pages, workspaces and every section, with section
  synonyms and recent picks. Navigation only.
- **Home dashboard**: summary figures that double as filters, filter, kind,
  state and sort in the URL, an access column, last activity per row and a
  recent activity list. `#/sandboxes` and `#/projects` are the same view for
  one kind.
- **`#/attention`** route and sidebar link for the Needs attention inbox; the
  sidebar badge is the inbox's own count (`useAttentionCount()`).
- The workspace view no longer draws its own sidebar or picker; it publishes
  its section badges to the shell. Section pills stay on phones.
- Legacy and typed hashes (`access`, `devlinks`, `activity`, `repositories`,
  `files`, `uploads`, `web`, `#/inbox`) still land.

## Still open

- A named access level per sandbox needs the control work's API; Home reads
  `accessLevel` or `level` from summaries or project status when present.
- History has no server-side feed across workspaces; recent activity polls
  each workspace once a minute, which is fine for a handful and should become
  one API call if the number grows.
- Orca projects are not in the palette (they need a fetch of the runtime's list).
