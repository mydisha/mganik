import { Menu } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, Outlet } from "react-router";
import { AccountMenu } from "@/features/auth/account-menu";
import { RequireSession, useSession } from "@/features/auth/session";
import { consoleNav } from "@/features/console/nav";
import { consoleStrings as t } from "@/features/console/strings";
import { strings } from "@/shared/lib/strings";
import { cn } from "@/shared/lib/utils";
import { Button } from "@/shared/ui/button";
import { SidePanel } from "@/shared/ui/side-panel";
import { Skeleton } from "@/shared/ui/skeleton";

/** Shell Command Center: hanya untuk staff yang sudah login. */
export function AppShell() {
  return (
    <RequireSession loading={<ShellSkeleton />}>
      <Shell />
    </RequireSession>
  );
}

function ConsoleNav({ onNavigate }: { onNavigate?: () => void }) {
  const me = useSession();
  const items = consoleNav.filter((item) => me.permissions.includes(item.permission));

  return (
    <nav aria-label={t.nav.label}>
      <ul className="flex flex-col gap-0.5">
        {items.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              onClick={onNavigate}
              className={({ isActive }) =>
                cn(
                  "flex min-h-9 items-center rounded-md px-3 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-accent-foreground max-lg:min-h-11",
                  isActive && "bg-accent text-foreground",
                )
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}

function Shell() {
  const [menuOpen, setMenuOpen] = useState(false);

  return (
    <div className="min-h-dvh">
      <a
        href="#konten"
        className="sr-only z-50 rounded-md bg-primary px-3 py-2 text-sm text-primary-foreground focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        {t.shell.skipToContent}
      </a>
      <header className="sticky top-0 z-40 border-b bg-card">
        <div className="flex h-14 items-center gap-2 px-4 md:px-6">
          <Button variant="ghost" className="lg:hidden" onClick={() => setMenuOpen(true)} aria-expanded={menuOpen}>
            <Menu aria-hidden="true" />
            {t.shell.menu}
          </Button>
          <Link to="/console" className="rounded-md px-1 text-base font-semibold">
            {strings.brand}
          </Link>
          <div className="ml-auto">
            <AccountMenu />
          </div>
        </div>
      </header>
      <div className="lg:grid lg:grid-cols-[15rem_minmax(0,1fr)]">
        <aside className="hidden border-r lg:block">
          <div className="sticky top-14 p-3">
            <ConsoleNav />
          </div>
        </aside>
        <main id="konten" tabIndex={-1} className="mx-auto w-full max-w-6xl px-4 py-6 outline-none md:px-6">
          <Outlet />
        </main>
      </div>
      <SidePanel open={menuOpen} onOpenChange={setMenuOpen} title={t.shell.menu} side="left">
        <ConsoleNav onNavigate={() => setMenuOpen(false)} />
      </SidePanel>
    </div>
  );
}

function ShellSkeleton() {
  return (
    <div className="min-h-dvh" aria-busy="true">
      <span className="sr-only" role="status">
        {t.shell.loading}
      </span>
      <div className="flex h-14 items-center gap-3 border-b bg-card px-4 md:px-6">
        <Skeleton className="h-5 w-20" />
        <Skeleton className="ml-auto h-8 w-28" />
      </div>
      <div className="lg:grid lg:grid-cols-[15rem_minmax(0,1fr)]">
        <div className="hidden space-y-2 border-r p-3 lg:block">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="h-8 w-full" />
          ))}
        </div>
        <div className="mx-auto w-full max-w-6xl space-y-6 px-4 py-6 md:px-6">
          <Skeleton className="h-7 w-48" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      </div>
    </div>
  );
}
