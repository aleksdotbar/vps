import {
  CubeIcon,
  FolderSimpleIcon,
  GearIcon,
  HardDrivesIcon,
  PulseIcon,
} from "@phosphor-icons/react";
import {
  Link,
  Outlet,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { queryClient, unauthenticated } from "./api";
import { Logout } from "./session";

function section(pathname: string) {
  if (/^\/(projects|environments|releases)(\/|$)/.test(pathname))
    return "projects";
  if (pathname.startsWith("/servers")) return "servers";
  if (pathname.startsWith("/operations")) return "activity";
  if (pathname.startsWith("/settings")) return "settings";
  return "";
}

export function Shell() {
  const current = section(
    useRouterState({ select: (state) => state.location.pathname }),
  );
  const mark = (name: string) =>
    current === name ? ({ "aria-current": "page" } as const) : {};
  return (
    <div className="app">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <aside className="sidebar">
        <Link className="brand" to="/projects" search={{ cursor: "" }}>
          <span className="brand-mark" aria-hidden="true">
            <CubeIcon size={16} weight="bold" />
          </span>
          <span className="brand-text">Panel</span>
        </Link>
        <nav className="nav" aria-label="Main navigation">
          <Link to="/projects" search={{ cursor: "" }} {...mark("projects")}>
            <FolderSimpleIcon size={18} aria-hidden="true" />
            <span>Projects</span>
          </Link>
          <Link to="/servers" search={{ cursor: "" }} {...mark("servers")}>
            <HardDrivesIcon size={18} aria-hidden="true" />
            <span>Servers</span>
          </Link>
          <Link
            to="/operations"
            search={{ environment: "", server: "", cursor: "" }}
            {...mark("activity")}
          >
            <PulseIcon size={18} aria-hidden="true" />
            <span>Activity</span>
          </Link>
          <Link to="/settings" {...mark("settings")}>
            <GearIcon size={18} aria-hidden="true" />
            <span>Settings</span>
          </Link>
        </nav>
        <div className="sidebar-footer">
          <Logout />
          <span className="hint">Private over Tailscale</span>
        </div>
      </aside>
      <main id="main" className="main" tabIndex={-1}>
        <Outlet />
      </main>
    </div>
  );
}

export function NavigationProgress() {
  const router = useRouter();
  const [navigating, setNavigating] = useState(false);

  useEffect(() => {
    const expiry = queryClient.getQueryCache().subscribe((event) => {
      if (
        event.type === "updated" &&
        event.action.type === "error" &&
        unauthenticated(event.action.error)
      ) {
        void (async () => {
          await queryClient.cancelQueries();
          queryClient.clear();
          await router.navigate({ to: "/login", replace: true });
        })();
      }
    });
    const start = router.subscribe("onBeforeNavigate", (event) => {
      if (event.fromLocation && event.hrefChanged) setNavigating(true);
    });
    const finish = router.subscribe("onResolved", () => setNavigating(false));
    const focus = router.subscribe("onRendered", (event) => {
      if (event.pathChanged) {
        document
          .querySelector<HTMLElement>("h1")
          ?.focus({ preventScroll: true });
      }
    });
    return () => {
      expiry();
      start();
      finish();
      focus();
    };
  }, [router]);

  return navigating ? (
    <div
      className="navigation-progress"
      role="progressbar"
      aria-label="Opening page"
    />
  ) : null;
}
