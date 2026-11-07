import { NavLink, Outlet } from "react-router";
import { strings } from "@/shared/lib/strings";
import { cn } from "@/shared/lib/utils";

const navItems = [
  { to: "/console", label: strings.nav.console },
  { to: "/pos", label: strings.nav.pos },
];

export function AppShell() {
  return (
    <div className="min-h-dvh">
      <header className="border-b bg-card">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-6 px-4 md:px-6">
          <span className="text-base font-semibold">{strings.brand}</span>
          <nav aria-label={strings.nav.label} className="flex gap-1">
            {navItems.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                className={({ isActive }) =>
                  cn(
                    "rounded-md px-3 py-2 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-accent-foreground max-md:min-h-11",
                    isActive && "bg-accent text-accent-foreground",
                  )
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-6 md:px-6">
        <Outlet />
      </main>
    </div>
  );
}
