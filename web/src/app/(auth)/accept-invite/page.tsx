import type { Metadata } from "next";

export const metadata: Metadata = { title: "Accept invitation" };

// The form arrives in FE-0.2.
export default function AcceptInvitePage() {
  return <h1 className="text-xl font-semibold">Accept invitation</h1>;
}
