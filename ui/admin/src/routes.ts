export type Tab = "dashboard" | "models" | "workers" | "billing" | "events" | "requests" | "configOps" | "advanced";

const paths: Record<Tab, string> = {
  dashboard: "/ui",
  models: "/ui/models",
  workers: "/ui/workers",
  billing: "/ui/billing",
  events: "/ui/event-log",
  requests: "/ui/request-log",
  configOps: "/ui/config",
  advanced: "/ui/advanced"
};

export function pathForTab(tab: Tab) {
  return paths[tab];
}

export function tabFromPath(pathname: string): Tab {
  return (Object.entries(paths).find(([, path]) => path === pathname)?.[0] as Tab | undefined) ?? "dashboard";
}
