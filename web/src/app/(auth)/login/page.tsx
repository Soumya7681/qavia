import type { Metadata } from "next";

export const metadata: Metadata = { title: "Sign in" };

// The form arrives in FE-0.2.
export default function LoginPage() {
  return <h1 className="text-xl font-semibold">Sign in</h1>;
}
