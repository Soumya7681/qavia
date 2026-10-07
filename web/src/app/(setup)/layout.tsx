// The first-run wizard. Reachable only until the first admin exists (FE-0.3).
export default function SetupLayout({ children }: { children: React.ReactNode }) {
  return <main className="flex flex-1 items-center justify-center p-6">{children}</main>;
}
