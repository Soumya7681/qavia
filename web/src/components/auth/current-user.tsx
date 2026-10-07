"use client";

import { createContext, useContext } from "react";

import { type CurrentUser, hasRole, type Role } from "@/lib/auth/roles";

const CurrentUserContext = createContext<CurrentUser | null>(null);

/** Set once by the (app) layout from the server's `GET /me`. */
export function CurrentUserProvider({
  user,
  children,
}: {
  user: CurrentUser;
  children: React.ReactNode;
}) {
  return <CurrentUserContext.Provider value={user}>{children}</CurrentUserContext.Provider>;
}

export function useCurrentUser(): CurrentUser {
  const user = useContext(CurrentUserContext);
  if (!user) {
    throw new Error("useCurrentUser is only available inside the (app) layout");
  }
  return user;
}

/**
 * Renders its children for `min` and above. Presentation only: the API decides,
 * and anything this hides must still handle a 403 if it is reached another way.
 */
export function ShowForRole({ min, children }: { min: Role; children: React.ReactNode }) {
  const user = useContext(CurrentUserContext);
  return hasRole(user, min) ? children : null;
}
