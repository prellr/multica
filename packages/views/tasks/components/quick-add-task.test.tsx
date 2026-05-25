import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enTasks from "../../locales/en/tasks.json";

const TEST_RESOURCES = { en: { common: enCommon, tasks: enTasks } };

// Mock the create-task mutation hook to capture call args without spinning
// up a real QueryClient + network roundtrip. The component cares about
// three contract points: (a) `mutate(input, { onError })` is called with
// the title trimmed, (b) the input is cleared on submit, (c) Escape clears
// without submitting.
const createMutate = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/tasks", () => ({
  useCreateTask: () => ({ mutate: createMutate }),
}));

vi.mock("sonner", () => ({
  toast: { error: toastError, success: vi.fn() },
}));

import { QuickAddTask } from "./quick-add-task";

function renderQuickAdd() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QuickAddTask />
    </I18nProvider>,
  );
}

beforeEach(() => {
  createMutate.mockReset();
  toastError.mockReset();
});

describe("QuickAddTask", () => {
  it("submits on Enter and clears the input", () => {
    renderQuickAdd();
    const input = screen.getByPlaceholderText(enTasks.quick_add.placeholder) as HTMLInputElement;

    fireEvent.change(input, { target: { value: "  pick up dry cleaning  " } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(createMutate).toHaveBeenCalledTimes(1);
    // Trim happens on the title — the API contract rejects pure whitespace
    // and the surface should keep that consistent on the client.
    const [vars] = createMutate.mock.calls[0]!;
    expect(vars).toEqual({ title: "pick up dry cleaning" });
    expect(input.value).toBe("");
  });

  it("is a no-op for empty / whitespace-only submissions", () => {
    renderQuickAdd();
    const input = screen.getByPlaceholderText(enTasks.quick_add.placeholder) as HTMLInputElement;

    fireEvent.keyDown(input, { key: "Enter" });
    expect(createMutate).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: "   " } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(createMutate).not.toHaveBeenCalled();
  });

  it("clears (without submitting) on Escape", () => {
    renderQuickAdd();
    const input = screen.getByPlaceholderText(enTasks.quick_add.placeholder) as HTMLInputElement;

    fireEvent.change(input, { target: { value: "abandoned" } });
    fireEvent.keyDown(input, { key: "Escape" });

    expect(createMutate).not.toHaveBeenCalled();
    expect(input.value).toBe("");
  });

  it("restores the input and toasts on mutation error", () => {
    // Simulate the create rejecting — useCreateTask invokes the per-call
    // onError handler that the component passes. The input should re-fill
    // so the user can retry without losing what they typed.
    createMutate.mockImplementation((_input, opts) => {
      opts?.onError?.(new Error("network down"));
    });

    renderQuickAdd();
    const input = screen.getByPlaceholderText(enTasks.quick_add.placeholder) as HTMLInputElement;

    fireEvent.change(input, { target: { value: "wifi off" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(createMutate).toHaveBeenCalledTimes(1);
    expect(toastError).toHaveBeenCalledWith(enTasks.errors.create_failed);
    expect(input.value).toBe("wifi off");
  });

  it("ignores Enter while IME composition is in progress", () => {
    // Without this guard, hitting Enter to confirm a Chinese / Japanese /
    // Korean character would pre-submit a half-typed title.
    renderQuickAdd();
    const input = screen.getByPlaceholderText(enTasks.quick_add.placeholder) as HTMLInputElement;

    fireEvent.change(input, { target: { value: "你好" } });
    fireEvent.compositionStart(input);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(createMutate).not.toHaveBeenCalled();

    fireEvent.compositionEnd(input);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(createMutate).toHaveBeenCalledTimes(1);
  });
});
