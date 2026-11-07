# mGanik interface design

Three surfaces, one token system:

| Surface | Route | Users | Character |
|---|---|---|---|
| Command Center | `/console` | Ops, warehouse, production, marketing | Calm and dense like an operations dashboard; tables, filters, work queues |
| Cashier | `/pos` | Store cashiers | Offline-first, large touch targets, fully usable with keyboard + scanner |
| Verification | `/v/:code` | Consumers coming from the hologram QR | Mobile-first (360 px), no login, one clear result |

The code is in `apps/web/src`. Tokens live in `app/styles.css`, shared components in `shared/ui`,
and all UI copy in `shared/lib/strings.ts`. If a feature needs something that is not here, add it
to this document first.

## Stack

React 19, Vite, TypeScript strict, Tailwind v4, shadcn/ui (new-york, Radix, CSS variables),
lucide, IBM Plex Sans and IBM Plex Mono (self-hosted through `@fontsource`), react-router 7,
TanStack Query, openapi-fetch with types from `packages/contracts/openapi.yaml`.

## Tokens

Every color is an `oklch` CSS variable, defined for light (`:root`) and dark (`.dark`), then exposed
to Tailwind through `@theme inline`. Components use token utilities (`bg-card`,
`text-muted-foreground`, `border-danger-border`), never hex, rgb, or Tailwind's built-in palette
such as `text-red-600`.

| Token | Used for |
|---|---|
| `background` / `foreground` | Page background and main text |
| `card` / `card-foreground` | Tables, cards, inputs |
| `popover` / `popover-foreground` | Dialogs, menus, tooltips |
| `primary` / `primary-foreground` | The one primary action per view |
| `secondary`, `accent`, `muted` (+ `-foreground`) | Hover, quiet surfaces, helper text. `muted-foreground` is the lightest text allowed |
| `destructive` / `destructive-foreground` | Actions that cancel, revoke, or delete |
| `border`, `input`, `ring` | Dividers, control borders (3:1 against `card`), focus ring |
| `link` | Links inside text |
| `overlay` | The dark layer behind dialogs and sheets |

Status tones (subtle background + text + border):

| Tone | Used for |
|---|---|
| `success` | Ready, synced, genuine, sold, accepted |
| `warning` | Pending, offline with a sync queue, low stock, needs review |
| `danger` | Failed, suspicious, a conflict that needs action, critical stock |
| `info` | In progress, in transit |
| `neutral` | Inactive, not checked yet, unknown status |

Every text/background pair is at least 4.5:1 in both themes; control borders and the focus ring are
at least 3:1. Recheck contrast every time a token changes.

### Typography

- Sans: IBM Plex Sans 400/500/600. Mono: IBM Plex Mono 400/500 for serial codes, ids, SKUs, and JSON.
- Base size 14 px (`text-sm`). Inputs use 16 px below 768 px so iOS does not zoom.
- Scale: page title `text-xl font-semibold` (one `h1`), section title `text-base font-semibold` (`h2`), body `text-sm`, helper text and table headers `text-xs`. No text below 12 px.
- Numbers that are compared (rupiah, qty, DOI) use `tabular-nums`.

### Spacing, radius, shadows

- 4 px grid. Page gutter 16 px on mobile, 24 px from 768 px. Sections stack with `gap-6`, controls inside a section with `gap-3`.
- Radius from `--radius` (8 px): `rounded-md` for controls, `rounded-lg` for cards and tables, `rounded-full` for badges.
- Flat shadows: `shadow-xs` for cards and controls, `shadow-md` for menus, `shadow-lg` for dialogs.

## Components

| Component | Use for |
|---|---|
| `Button` | Every action. Variants `default` (one per view), `outline`, `ghost`, `secondary`, `destructive`, `link`. Every size reaches 44 px below 768 px |
| `StatusBadge` | Every status. Always text; color only reinforces it |
| `PageHeader` | The `h1`, one sentence of context, page actions |
| `EmptyState` | No data yet: what will appear here and the next step |

## Command Center

- The summary is written as a sentence before the tables (in the UI's Indonesian, for example "3 SKU akan habis dalam 5 hari di 2 toko", meaning "3 SKUs will run out within 5 days at 2 stores").
- At most three blocks per page. Every fact appears once.
- Tables: 40 px rows, headers `text-xs text-muted-foreground`, numbers right-aligned. Below 768 px rows become cards; the page never scrolls sideways.

## Cashier

- Touch targets at least 48 px; primary action buttons 56–64 px; numpad 64 × 64 px.
- The scan field always gets focus back after a dialog closes.
- Sync status is always visible: icon + text (for example "Offline · 12 transaksi menunggu", meaning "Offline · 12 transactions waiting"), never color alone.
- Keyboard shortcuts are shown on their buttons (F2 pay, F4 delete line, Esc cancel).
- Scan feedback: distinct short sounds for success and failure, plus a row highlight.
- Target screens: 1024 × 768 landscape and 1366 × 768.

## Consumer verification

- One large, clear result at the top: genuine, verified before (with date and city), suspicious, not registered, or recalled.
- Internal statuses are never shown to the consumer.
- Location permission is requested with a one-sentence explanation; if it is denied, verification still works.

## Formats

- Rupiah: whole numbers, `Rp150.000` (no space, dot as thousands separator), `Rp0` for zero.
- Numbers: `id-ID` grouping (`1.234`).
- Time: stored in UTC, shown in the location's time zone (WIB/WITA/WIT) with the `id-ID` format and a zone suffix.
- Serial codes and ids: mono, copyable.
- Empty values: `—`.

## Copy

- UI copy is in Indonesian, in sentence case (not Title Case), short and specific.
- Say what happened and what comes next, for example "Sinkronisasi gagal. Transaksi aman di perangkat; akan dicoba lagi otomatis." ("Sync failed. Transactions are safe on the device; it will retry automatically.")

## States

Every screen has a loading state (a skeleton the size of the content), an empty state, an error
state (with "Coba lagi", "Try again"), and, for the cashier, offline and conflict states.

## Dark mode

The `dark` class on `<html>` swaps every token; components need no `dark:` variants. The theme
follows the system and is applied before the first render. Check every new screen in both themes.

## Accessibility

- Every input has a visible label; icon buttons have an `aria-label`.
- Focus is always visible (`focus-visible` from the `ring` token).
- Status is never color alone.
- One `h1` per page; sections use `h2`.
