import { Sidebar } from "@cloudflare/kumo";
import {
  CubeIcon,
  FolderSimpleIcon,
  GearIcon,
  HardDrivesIcon,
  PulseIcon,
} from "@phosphor-icons/react";
import { Outlet, useRouter, useRouterState } from "@tanstack/react-router";
import { useEffect } from "react";

import { queryClient, unauthenticated } from "./api";
import { Logout } from "./session";

const sections = [
  { href: "/projects", label: "Projects", icon: FolderSimpleIcon },
  { href: "/servers", label: "Servers", icon: HardDrivesIcon },
  { href: "/operations", label: "Activity", icon: PulseIcon },
  { href: "/settings", label: "Settings", icon: GearIcon },
];

function section(pathname: string) {
  if (/^\/(projects|environments|releases)(\/|$)/.test(pathname))
    return "/projects";
  return sections.find(({ href }) => pathname.startsWith(href))?.href;
}

function Brand() {
  return (
    <span className="flex items-center gap-2 px-3 text-sm font-semibold text-kumo-strong">
      <CubeIcon size={20} weight="bold" aria-hidden="true" />
      Panel
    </span>
  );
}

export function Shell() {
  const current = section(
    useRouterState({ select: (state) => state.location.pathname }),
  );
  return (
    <Sidebar.Provider defaultOpen collapsible="offcanvas">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50"
      >
        Skip to content
      </a>
      {/* Full height and pinned while the page scrolls; below md kumo renders a sheet instead. */}
      <Sidebar className="md:sticky md:top-0 md:h-svh">
        <Sidebar.Header>
          <Brand />
        </Sidebar.Header>
        <Sidebar.Content>
          <Sidebar.Group>
            <Sidebar.Menu>
              {sections.map(({ href, label, icon }) => (
                <Sidebar.MenuButton
                  key={href}
                  href={href}
                  icon={icon}
                  active={current === href}
                >
                  {label}
                </Sidebar.MenuButton>
              ))}
            </Sidebar.Menu>
          </Sidebar.Group>
        </Sidebar.Content>
        <Sidebar.Footer>
          <Logout />
        </Sidebar.Footer>
      </Sidebar>
      <main id="main" tabIndex={-1} className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2 border-b border-kumo-line p-2 md:hidden">
          <Sidebar.Trigger />
          <Brand />
        </div>
        <Outlet />
      </main>
    </Sidebar.Provider>
  );
}

/** YouTube-style bar while the router loads the next page; the current page stays visible. */
export function NavigationProgress() {
  const loading = useRouterState({
    select: (state) => state.status === "pending",
  });
  return loading ? (
    <div
      role="progressbar"
      aria-label="Loading page"
      className="fixed inset-x-0 top-0 z-50 h-0.5 origin-left scale-x-90 bg-kumo-brand transition-transform delay-150 duration-[8s] ease-out starting:scale-x-0"
    />
  ) : null;
}

/** Signs out once any query reports the session ended, and moves focus to each new page's heading. */
export function RouteEffects() {
  const router = useRouter();
  useEffect(() => {
    const expiry = queryClient.getQueryCache().subscribe((event) => {
      if (
        event.type === "updated" &&
        event.action.type === "error" &&
        unauthenticated(event.action.error)
      )
        void (async () => {
          await queryClient.cancelQueries();
          queryClient.clear();
          await router.navigate({ to: "/login", replace: true });
        })();
    });
    const focus = router.subscribe("onRendered", (event) => {
      if (event.pathChanged)
        document.getElementById("main")?.focus({ preventScroll: true });
    });
    return () => {
      expiry();
      focus();
    };
  }, [router]);
  return null;
}
