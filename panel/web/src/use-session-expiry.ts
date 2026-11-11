import { useRouter } from "@tanstack/react-router";

import { queryClient, unauthenticated } from "./api";

export function useSessionExpiry() {
  const router = useRouter();
  return async (error: unknown) => {
    if (!unauthenticated(error)) return false;
    await queryClient.cancelQueries();
    queryClient.clear();
    await router.navigate({ to: "/login", replace: true });
    return true;
  };
}
