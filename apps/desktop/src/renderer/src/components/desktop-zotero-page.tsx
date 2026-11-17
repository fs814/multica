import { ZoteroPage } from "@multica/views/zotero";

export function DesktopZoteroPage() {
  return <ZoteroPage bridge={window.desktopAPI?.zotero} />;
}
