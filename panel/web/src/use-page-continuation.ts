import { useRouter } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

export function usePageContinuation() {
  const router = useRouter();
  const page = useRef({ mounted: false, navigation: 0 });
  useEffect(() => {
    const owner = page.current;
    owner.mounted = true;
    const unsubscribe = router.subscribe("onBeforeNavigate", () => {
      owner.navigation += 1;
    });
    return () => {
      owner.mounted = false;
      unsubscribe();
    };
  }, [router]);

  function capturePage() {
    const location = router.latestLocation;
    const navigation = page.current.navigation;
    // Retained pages stay mounted while a newer destination loads.
    return () =>
      page.current.mounted &&
      page.current.navigation === navigation &&
      router.latestLocation === location;
  }

  function isMounted() {
    return page.current.mounted;
  }

  return { capturePage, isMounted };
}
