import type { Metadata } from "next";

export const metadata: Metadata = { title: "Projects" };

// The landing page comes from the user's default landing setting in FE-0.1.
export default function HomePage() {
  return <h1 className="text-xl font-semibold">Projects</h1>;
}
