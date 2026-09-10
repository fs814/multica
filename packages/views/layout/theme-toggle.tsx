"use client";
import { Sun, Moon } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { useT } from "../i18n";
export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const { t } = useT("settings");
  const dark = resolvedTheme === "dark";
  const label = dark
    ? t(($) => $.preferences.theme.light)
    : t(($) => $.preferences.theme.dark);
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      onClick={() => setTheme(dark ? "light" : "dark")}
    >
      <Sun className="hidden size-4 dark:block" />
      <Moon className="size-4 dark:hidden" />
    </Button>
  );
}
