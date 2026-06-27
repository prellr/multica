"use client";

import { ShipShell } from "@multica/views/ship";

/**
 * Ship layout — wraps the Ship board and release-detail pages in the
 * persistent Concierge drawer shell. Mounting the shell here (rather
 * than inside each page) keeps the drawer + its channel conversation
 * docked across `/ship` → `/ship/release/[id]` navigation.
 */
export default function ShipLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return <ShipShell>{children}</ShipShell>;
}
