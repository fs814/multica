import { WebPage } from "@multica/views/web-links";
import { useNavigation } from "@multica/views/navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWebLinksStore } from "@multica/core/web-links";

export function DesktopWebPage() {
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  return <WebPage openSite={async (url) => {
    const site = useWebLinksStore.getState().links.find((link) => link.url === url);
    if (site) navigation.openInNewTab?.(paths.webSite(site.id), site.name, { activate: true });
  }} />;
}
