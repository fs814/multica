import { ToolsPage } from "@multica/views/tools";

export function DesktopToolsPage() {
  return <ToolsPage bridge={window.desktopAPI?.tools} />;
}
