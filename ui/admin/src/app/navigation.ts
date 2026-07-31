export type Tab = "dashboard" | "models" | "workers" | "billing" | "events" | "requests" | "configOps" | "advanced";

export type NavItem = {
  id: Tab;
  label: string;
};

export type NavGroup = {
  label: string;
  items: NavItem[];
};

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

export const NAV_GROUPS: NavGroup[] = [
  { label: "Signal", items: [{ id: "dashboard", label: "Overview" }] },
  {
    label: "Fleet",
    items: [
      { id: "models", label: "Models" },
      { id: "workers", label: "Workers" }
    ]
  },
  {
    label: "Observe",
    items: [
      { id: "billing", label: "Billing" },
      { id: "events", label: "Events" },
      { id: "requests", label: "Requests" }
    ]
  },
  {
    label: "Configure",
    items: [
      { id: "configOps", label: "Config" },
      { id: "advanced", label: "Advanced" }
    ]
  }
];

export function pathForTab(tab: Tab) {
  return paths[tab];
}

export function shouldPushTabPath(currentPath: string, tab: Tab) {
  return currentPath !== pathForTab(tab);
}

export function tabFromPath(pathname: string): Tab {
  return (Object.entries(paths).find(([, path]) => path === pathname)?.[0] as Tab | undefined) ?? "dashboard";
}
