"use client";

import { CheckIcon, ChevronsUpDownIcon, FolderIcon, PlusIcon } from "lucide-react";
import { useParams, useRouter } from "next/navigation";
import { useState } from "react";

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { useCursorList } from "@/lib/hooks/use-cursor-list";

/**
 * Jump between projects from anywhere. Lists the projects the API says this user
 * can see; filtering is over what has loaded, with "Load more" for the rest.
 */
export function ProjectSwitcher() {
  const router = useRouter();
  const params = useParams<{ projectID?: string }>();
  const [open, setOpen] = useState(false);
  const projects = useCursorList("/api/v1/projects", { query: { limit: 50 } });
  const current = projects.items.find((p) => p.id === params.projectID);

  const go = (href: string) => {
    setOpen(false);
    router.push(href);
  };

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <SidebarMenuButton
              size="lg"
              role="combobox"
              aria-expanded={open}
              aria-label="Switch project"
              className="data-[state=open]:bg-sidebar-accent"
            >
              <span
                aria-hidden
                className="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground"
              >
                <FolderIcon className="size-4" />
              </span>
              <span className="grid flex-1 text-left leading-tight">
                <span className="text-xs text-muted-foreground">Project</span>
                <span className="truncate text-sm font-medium">
                  {current?.name ?? (params.projectID ? "…" : "None selected")}
                </span>
              </span>
              <ChevronsUpDownIcon aria-hidden className="ml-auto size-4 text-muted-foreground" />
            </SidebarMenuButton>
          </PopoverTrigger>
          <PopoverContent className="w-72 p-0" align="start">
            <Command>
              <CommandInput placeholder="Find a project" />
              <CommandList>
                <CommandEmpty>
                  {projects.isLoading ? "Loading projects…" : "No project matches."}
                </CommandEmpty>
                <CommandGroup>
                  {projects.items.map((project) => (
                    <CommandItem
                      key={project.id}
                      value={`${project.name} ${project.id}`}
                      onSelect={() => go(`/projects/${project.id}`)}
                    >
                      <span className="truncate">{project.name}</span>
                      {project.archived ? (
                        <span className="ml-1 text-xs text-muted-foreground">archived</span>
                      ) : null}
                      {project.id === params.projectID ? (
                        <CheckIcon aria-hidden className="ml-auto size-4" />
                      ) : null}
                    </CommandItem>
                  ))}
                  {projects.hasMore ? (
                    <CommandItem onSelect={() => void projects.loadMore()} value="__load_more__">
                      {projects.isLoadingMore ? "Loading…" : "Load more projects"}
                    </CommandItem>
                  ) : null}
                </CommandGroup>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem onSelect={() => go("/projects?new=1")} value="__new_project__">
                    <PlusIcon aria-hidden /> New project
                  </CommandItem>
                  <CommandItem onSelect={() => go("/projects")} value="__all_projects__">
                    All projects
                  </CommandItem>
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
