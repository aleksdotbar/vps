import {
  Link,
  Outlet,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import {
  ActivityIcon,
  BoxIcon,
  FolderIcon,
  ServerIcon,
  SettingsIcon,
} from "lucide-react";
import { useEffect } from "react";

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from "@/components/ui/sidebar";

import { queryClient, unauthenticated } from "./api";
import { Logout } from "./session";

const sections = [
  { to: "/projects", label: "Projects", icon: FolderIcon },
  { to: "/servers", label: "Servers", icon: ServerIcon },
  { to: "/operations", label: "Activity", icon: ActivityIcon },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
] as const;

function section(pathname: string) {
  if (/^\/(projects|environments|releases)(\/|$)/.test(pathname))
    return "/projects";
  return sections.find(({ to }) => pathname.startsWith(to))?.to;
}

export function Shell() {
  const current = section(
    useRouterState({ select: (state) => state.location.pathname }),
  );
  return (
    <SidebarProvider>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50"
      >
        Skip to content
      </a>
      <Sidebar variant="inset">
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" render={<Link to="/projects" />}>
                <BoxIcon />
                <span className="font-semibold">Panel</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {sections.map(({ to, label, icon: Icon }) => (
                  <SidebarMenuItem key={to}>
                    <SidebarMenuButton
                      isActive={current === to}
                      render={<Link to={to} />}
                    >
                      <Icon />
                      <span>{label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
        <SidebarFooter>
          <Logout />
        </SidebarFooter>
      </Sidebar>
      {/* Focus target for route changes and the skip link; it is not interactive, so no outline. */}
      <SidebarInset id="main" tabIndex={-1} className="outline-none">
        <Outlet />
      </SidebarInset>
    </SidebarProvider>
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
      className="fixed inset-x-0 top-0 z-50 h-0.5 origin-left scale-x-90 bg-primary transition-transform delay-150 duration-[8s] ease-out starting:scale-x-0"
    />
  ) : null;
}

/** Signs out once any query reports the session ended, and moves focus to each new page. */
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
