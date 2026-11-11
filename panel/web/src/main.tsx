import "./styles.css";

import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode, Suspense } from "react";
import { createRoot } from "react-dom/client";

import { queryClient, transport } from "./api";
import { InitialLoading } from "./components";
import { router } from "./router";

const root = document.getElementById("root");
if (!root) throw new Error("Panel root is missing");

createRoot(root).render(
  <StrictMode>
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <Suspense fallback={<InitialLoading />}>
          <RouterProvider router={router} />
        </Suspense>
      </QueryClientProvider>
    </TransportProvider>
  </StrictMode>,
);
