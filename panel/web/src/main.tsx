import "./theme";
import "./styles.css";

import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, useRouterState } from "@tanstack/react-router";
import { StrictMode, Suspense } from "react";
import { createRoot } from "react-dom/client";

import { queryClient, transport } from "./api";
import { InitialLoading } from "./components";
import { router } from "./router";

// The router renders nothing until its first load resolves; navigations later
// keep the current page, so only this first load needs a placeholder.
function FirstLoad() {
  const loaded = useRouterState({
    router,
    select: (state) => !!state.resolvedLocation,
  });
  return loaded ? null : <InitialLoading />;
}

const root = document.getElementById("root");
if (!root) throw new Error("Panel root is missing");

createRoot(root).render(
  <StrictMode>
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <Suspense fallback={<InitialLoading />}>
          <FirstLoad />
          <RouterProvider router={router} />
        </Suspense>
      </QueryClientProvider>
    </TransportProvider>
  </StrictMode>,
);
