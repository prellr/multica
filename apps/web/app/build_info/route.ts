import { NextResponse } from "next/server";

// Frontend mirror of the backend's /build_info
// (server/cmd/server/build_info.go). The deploy workflow curls this to
// verify the running frontend container actually serves the SHA that
// was just built — closing the frontend-only-deploy verification gap:
// the backend has had SHA verification since PR #54, but a frontend-only
// change (e.g. ROA-264) had no equivalent check, so a stale frontend
// image could ride through a green deploy unnoticed.
//
// BUILD_COMMIT is injected as a runtime ENV in the runner stage of
// Dockerfile.web (set from the MULTICA_BUILD_COMMIT compose build arg).
// It defaults to "unknown" for local/dev builds; the deploy verifier
// treats "unknown" as a hard failure — a dev build must never pass as
// a production deploy.
//
// force-dynamic so the value is read from the container's environment
// at request time, not frozen at `next build` time.
export const dynamic = "force-dynamic";

export function GET() {
  return NextResponse.json({
    commit: process.env.BUILD_COMMIT ?? "unknown",
    version: process.env.NEXT_PUBLIC_APP_VERSION ?? "dev",
  });
}
