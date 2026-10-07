"use client";

import { useQueryClient } from "@tanstack/react-query";
import {
  ChevronsUpDownIcon,
  KeyRoundIcon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  SunIcon,
} from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";
import { toast } from "sonner";

import { useCurrentUser } from "@/components/auth/current-user";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { api } from "@/lib/api/client";
import { useUpdatePreference } from "@/lib/hooks/use-update-preference";

export const roleLabel = {
  admin: "Admin",
  qa_lead: "QA Lead",
  qa_engineer: "QA Engineer",
  viewer: "Viewer",
} as const;

function initials(name: string) {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("");
}

const subscribe = () => () => {};

export function UserMenu() {
  const user = useCurrentUser();
  const router = useRouter();
  const queryClient = useQueryClient();
  const { theme, setTheme } = useTheme();
  const hydrated = useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );
  const preference = useUpdatePreference();

  const logout = api.useMutation("post", "/api/v1/auth/logout", {
    onSettled: () => {
      // Whatever the API said, nothing from this session may stay on screen.
      queryClient.clear();
      router.replace("/login");
    },
  });

  const chooseTheme = async (value: string) => {
    setTheme(value);
    try {
      await preference.update("preferences.theme", value);
    } catch {
      toast.error("Theme changed here, but could not be saved to your profile.");
    }
  };

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton size="lg" className="data-[state=open]:bg-sidebar-accent">
              <span
                aria-hidden
                className="flex size-8 shrink-0 items-center justify-center rounded-md bg-accent text-xs font-semibold text-accent-foreground"
              >
                {initials(user.name) || "?"}
              </span>
              <span className="grid flex-1 text-left leading-tight">
                <span className="truncate text-sm font-medium">{user.name}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {roleLabel[user.role]}
                </span>
              </span>
              <ChevronsUpDownIcon aria-hidden className="ml-auto size-4 text-muted-foreground" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-60">
            <DropdownMenuLabel className="font-normal">
              <span className="block truncate text-sm font-medium">{user.name}</span>
              <span className="block truncate text-xs text-muted-foreground">{user.email}</span>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-xs text-muted-foreground">Theme</DropdownMenuLabel>
            <DropdownMenuRadioGroup
              value={hydrated ? theme : undefined}
              onValueChange={chooseTheme}
            >
              <DropdownMenuRadioItem value="light">
                <SunIcon aria-hidden /> Light
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="dark">
                <MoonIcon aria-hidden /> Dark
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="system">
                <MonitorIcon aria-hidden /> System
              </DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <Link href="/account">
                <KeyRoundIcon aria-hidden /> Account and password
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => logout.mutate({})} disabled={logout.isPending}>
              <LogOutIcon aria-hidden /> Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
