// @vitest-environment happy-dom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InboxIcon } from "lucide-react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "./button";
import { EmptyState } from "./empty-state";
import { ErrorState } from "./error-state";

describe("ErrorState", () => {
  it("shows the API's message and the incident ID, and retries", async () => {
    const retry = vi.fn();
    render(
      <ErrorState
        error={{
          code: "internal_error",
          message: "The run list could not be loaded.",
          incidentId: "inc_42",
        }}
        onRetry={retry}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("The run list could not be loaded.");
    expect(screen.getByText("inc_42")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("offers no retry it cannot perform", () => {
    render(<ErrorState error={{ message: "Gone." }} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText(/Incident ID/)).not.toBeInTheDocument();
  });
});

describe("EmptyState", () => {
  it("says what to do next", () => {
    render(
      <EmptyState
        icon={InboxIcon}
        title="No runs yet"
        description="Run the suite against staging."
        action={<Button>Run suite</Button>}
      />,
    );
    expect(screen.getByRole("heading", { name: "No runs yet" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run suite" })).toBeInTheDocument();
  });
});
