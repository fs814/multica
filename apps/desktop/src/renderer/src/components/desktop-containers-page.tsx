import { ContainersPage } from "@multica/views/containers";

export function DesktopContainersPage() {
  return <ContainersPage bridge={window.desktopAPI?.containers} />;
}
