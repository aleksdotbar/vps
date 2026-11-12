import "./styles.css";

import {
  LinkProvider,
  type LinkComponentProps,
  Toasty,
} from "@cloudflare/kumo";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { forwardRef, StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { queryClient, transport } from "./api";
import { router } from "./router";

// kumo links take a plain href; route same-origin clicks through the router.
const RouterLink = forwardRef<HTMLAnchorElement, LinkComponentProps>(
  // Some kumo parts (Breadcrumbs) still pass the deprecated `to` instead of `href`.
  function RouterLink({ href: anchor, to, onClick, ...props }, ref) {
    const href = anchor ?? to ?? "";
    return (
      <a
        ref={ref}
        href={href}
        {...props}
        onClick={(event) => {
          onClick?.(event);
          if (
            event.defaultPrevented ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey ||
            props.target ||
            !href.startsWith("/")
          )
            return;
          event.preventDefault();
          void router.navigate({ href });
        }}
      />
    );
  },
);

const root = document.getElementById("root");
if (!root) throw new Error("Panel root is missing");

createRoot(root).render(
  <StrictMode>
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <LinkProvider component={RouterLink}>
          <Toasty>
            <RouterProvider router={router} />
          </Toasty>
        </LinkProvider>
      </QueryClientProvider>
    </TransportProvider>
  </StrictMode>,
);
