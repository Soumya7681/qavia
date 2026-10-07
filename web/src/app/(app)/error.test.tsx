// @vitest-environment happy-dom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/errors";

import AppError from "./error";

describe("app error boundary", () => {
  it("shows an API error's message and incident ID", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const reset = vi.fn();
    render(
      <AppError
        error={
          new ApiError({
            code: "internal_error",
            status: 500,
            message: "Runs could not be listed.",
            incidentId: "inc_9",
          })
        }
        reset={reset}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Runs could not be listed.");
    expect(screen.getByText("inc_9")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(reset).toHaveBeenCalledOnce();
  });

  it("shows a server error's digest without leaking its message", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const error = Object.assign(new Error("pq: relation does not exist"), { digest: "4096123" });
    render(<AppError error={error} reset={() => {}} />);
    expect(screen.getByText("4096123")).toBeInTheDocument();
    expect(screen.queryByText(/relation does not exist/)).not.toBeInTheDocument();
  });
});
