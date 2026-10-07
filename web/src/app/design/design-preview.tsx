"use client";

import { InboxIcon, PlayIcon } from "lucide-react";
import { toast } from "sonner";

import { ThemeSwitch } from "@/components/theme-switch";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const cases = [
  {
    id: "TC-0142",
    title: "Create order rejects a negative quantity",
    endpoint: "POST /orders",
    status: "passed",
  },
  {
    id: "TC-0143",
    title: "Order total includes regional tax",
    endpoint: "GET /orders/{id}",
    status: "failed",
  },
  {
    id: "TC-0144",
    title: "Expired token returns 401 with an error code",
    endpoint: "GET /me",
    status: "flaky",
  },
  {
    id: "TC-0145",
    title: "Pagination cursor survives a concurrent insert",
    endpoint: "GET /orders",
    status: "queued",
  },
] as const;

const statusBadge = {
  passed: <Badge variant="success">Passed</Badge>,
  failed: <Badge variant="destructive">Failed</Badge>,
  flaky: <Badge variant="warning">Flaky</Badge>,
  queued: <Badge variant="info">Queued</Badge>,
};

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">{title}</h2>
      <div className="rounded-lg border bg-card p-4 sm:p-6">{children}</div>
    </section>
  );
}

export function DesignPreview() {
  return (
    <main className="mx-auto flex w-full max-w-5xl flex-col gap-8 px-4 py-8 sm:px-6">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Design system</h1>
          <p className="text-sm text-muted-foreground">
            Every primitive, in the current theme. Switch to check the other.
          </p>
        </div>
        <ThemeSwitch />
      </header>

      <Section title="Buttons">
        <div className="flex flex-wrap items-center gap-2">
          <Button>
            <PlayIcon data-icon="inline-start" aria-hidden />
            Run suite
          </Button>
          <Button variant="secondary">Save draft</Button>
          <Button variant="outline">Export</Button>
          <Button variant="ghost">Cancel</Button>
          <Button variant="destructive">Delete project</Button>
          <Button variant="link">View contract</Button>
          <Button disabled>Generating…</Button>
        </div>
      </Section>

      <Section title="Status badges">
        <div className="flex flex-wrap gap-2">
          {Object.values(statusBadge)}
          <Badge variant="outline">Draft</Badge>
          <Badge variant="secondary">v3</Badge>
        </div>
      </Section>

      <Section title="Form controls">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor="target">Staging URL</Label>
            <Input id="target" type="url" placeholder="https://staging.example.com" />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="name-invalid">Project name</Label>
            <Input id="name-invalid" aria-invalid defaultValue="" aria-describedby="name-error" />
            <p id="name-error" className="text-xs text-destructive">
              A project needs a name.
            </p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="test-type">Test type</Label>
            <Select defaultValue="api">
              <SelectTrigger id="test-type" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="api">API tests</SelectItem>
                <SelectItem value="ui">UI tests</SelectItem>
                <SelectItem value="perf">Performance</SelectItem>
                <SelectItem value="security">Security</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex items-center gap-2 self-end pb-2">
            <Checkbox id="review" defaultChecked />
            <Label htmlFor="review">Review extracted requirements before generating</Label>
          </div>
        </div>
      </Section>

      <Section title="Table">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-24">ID</TableHead>
              <TableHead>Test case</TableHead>
              <TableHead className="hidden md:table-cell">Endpoint</TableHead>
              <TableHead className="text-right">Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {cases.map((c) => (
              <TableRow key={c.id}>
                <TableCell className="font-mono text-xs">{c.id}</TableCell>
                <TableCell>{c.title}</TableCell>
                <TableCell className="hidden font-mono text-xs text-muted-foreground md:table-cell">
                  {c.endpoint}
                </TableCell>
                <TableCell className="text-right">{statusBadge[c.status]}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Section>

      <Section title="Tabs">
        <Tabs defaultValue="cases">
          <TabsList>
            <TabsTrigger value="cases">Cases</TabsTrigger>
            <TabsTrigger value="code">Code</TabsTrigger>
            <TabsTrigger value="runs">Runs</TabsTrigger>
          </TabsList>
          <TabsContent value="cases" className="pt-3 text-sm text-muted-foreground">
            42 approved, 6 waiting for review.
          </TabsContent>
          <TabsContent value="code" className="pt-3 text-sm text-muted-foreground">
            orders.test.ts, auth.test.ts, and a Postman collection.
          </TabsContent>
          <TabsContent value="runs" className="pt-3 text-sm text-muted-foreground">
            Last run 14 minutes ago against staging.
          </TabsContent>
        </Tabs>
      </Section>

      <Section title="Overlays and feedback">
        <div className="flex flex-wrap gap-2">
          <Dialog>
            <DialogTrigger asChild>
              <Button variant="outline">Approve cases</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Approve 6 test cases?</DialogTitle>
                <DialogDescription>
                  Approved cases are turned into runnable test code. You can still edit them
                  afterwards.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <DialogClose asChild>
                  <Button variant="ghost">Cancel</Button>
                </DialogClose>
                <DialogClose asChild>
                  <Button>Approve and generate</Button>
                </DialogClose>
              </DialogFooter>
            </DialogContent>
          </Dialog>

          <Sheet>
            <SheetTrigger asChild>
              <Button variant="outline">Run details</Button>
            </SheetTrigger>
            <SheetContent>
              <SheetHeader>
                <SheetTitle>Run #318</SheetTitle>
                <SheetDescription>48 tests against staging, finished in 2m 14s.</SheetDescription>
              </SheetHeader>
            </SheetContent>
          </Sheet>

          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="outline">Hover for help</Button>
            </TooltipTrigger>
            <TooltipContent>Retries a failed test once before marking it flaky.</TooltipContent>
          </Tooltip>

          <Button
            variant="outline"
            onClick={() =>
              toast.success("Suite queued", {
                description: "You will be notified when it finishes.",
              })
            }
          >
            Success toast
          </Button>
          <Button
            variant="outline"
            onClick={() =>
              toast.error("Run could not start", {
                description: "The target is not on the allowlist.",
              })
            }
          >
            Error toast
          </Button>
        </div>
      </Section>

      <Section title="Loading, empty, and error states">
        <div className="grid gap-4 lg:grid-cols-3">
          <div className="flex flex-col gap-2" aria-busy="true" aria-label="Loading test cases">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-5/6" />
            <Skeleton className="h-4 w-1/2" />
          </div>
          <EmptyState
            icon={InboxIcon}
            title="No runs yet"
            description="Approve the generated cases, then run the suite against your staging target."
            action={<Button size="sm">Run suite</Button>}
          />
          <ErrorState
            error={{
              code: "internal",
              message: "The run list could not be loaded. The problem was logged.",
              incidentId: "inc_7f3a9c21",
            }}
            onRetry={() => toast("Retrying…")}
          />
        </div>
      </Section>
    </main>
  );
}
