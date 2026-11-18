import { AppsPage } from "@multica/views/apps";

export function DesktopAppsPage() {
  return <AppsPage bridge={window.desktopAPI?.apps} />;
}
