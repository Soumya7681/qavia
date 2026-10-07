import type { Metadata } from "next";

import { QueryProvider } from "@/components/query-provider";
import { ThemeProvider } from "@/components/theme-provider";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Qavia", template: "%s · Qavia" },
  description: "Your AI QA engineer.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    // The theme class is set on <html> by a script before hydration, so the server
    // markup legitimately differs from the client's on that one attribute.
    <html lang="en" className="h-full antialiased" suppressHydrationWarning>
      <body className="flex min-h-full flex-col">
        <ThemeProvider>
          <QueryProvider>
            <TooltipProvider>
              {children}
              <Toaster />
            </TooltipProvider>
          </QueryProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
