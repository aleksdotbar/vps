import { Link, Outlet, useRouter } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { queryClient, unauthenticated } from "./api";
import { Logout } from "./session";

export function Shell() {
  return (
    <div className="app">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="app-header">
        <Link className="brand" to="/projects" search={{ cursor: "" }}>
          Hosting panel
        </Link>
        <span className="private-label">Private installation</span>
        <Logout />
      </header>
      <div className="app-body">
        <aside>
          <nav aria-label="Main navigation">
            <Link
              to="/projects"
              search={{ cursor: "" }}
              activeOptions={{ exact: false }}
              activeProps={{ "aria-current": "page" }}
            >
              Projects
            </Link>
            <Link
              to="/servers"
              search={{ cursor: "" }}
              activeOptions={{ exact: false }}
              activeProps={{ "aria-current": "page" }}
            >
              Servers
            </Link>
            <Link
              to="/operations"
              search={{ environment: "", server: "", cursor: "" }}
              activeOptions={{ exact: false }}
              activeProps={{ "aria-current": "page" }}
            >
              Operations
            </Link>
            <Link to="/settings" activeProps={{ "aria-current": "page" }}>
              Settings
            </Link>
          </nav>
        </aside>
        <main id="main" className="main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
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
