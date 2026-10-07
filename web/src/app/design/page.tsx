import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { DesignPreview } from "./design-preview";

export const metadata: Metadata = { title: "Design system", robots: { index: false } };

// Every primitive in one place, in both themes, for review while building
// (FE-S.2). Development only: production has no reason to serve it.
export default function DesignPage() {
  if (process.env.NODE_ENV === "production") {
    notFound();
  }
  return <DesignPreview />;
}
