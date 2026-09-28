import { screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as m from "../../paraglide/messages";
import { AddRepoModal } from "./AddRepoModal";
import { renderWithProviders } from "../../test/render";
import { authenticatedFetch, backrestService } from "../../api/client";
import { aegisStorageEnv } from "../../state/aegis";
import { alerts } from "../../components/common/Alerts";
import { makeConfig, makeRepo, connectError, Code } from "../../test/proto";
import { CheckRepoExistsResponseSchema } from "../../../gen/ts/v1/service_pb";
import { ResticSnapshotListSchema } from "../../../gen/ts/v1/restic_pb";
import { StringListSchema } from "../../../gen/ts/types/value_pb";

// The URIAutocomplete combobox is the only <input role="combobox"> in the
// modal (the Advanced-section EnumSelectors render as <button> triggers).
const getUriInput = (): HTMLInputElement =>
  screen
    .getAllByRole("combobox")
    .find((el) => el.tagName === "INPUT") as HTMLInputElement;

const getPasswordInput = (): HTMLInputElement =>
  screen.getByLabelText(m.login_password_placeholder()) as HTMLInputElement;

const AEGIS_URI =
  "s3:https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/aegis-abc123-apac-xyz789/servers/agt_exampleid001";

// What /aegis/status offers: the storages the server was installed with.
const aegisStatus = {
  enabled: true,
  state: "connected",
  pending: 0,
  instanceId: "test-instance",
  repositories: [{ name: "Asia primary", region: "apac", uri: AEGIS_URI }],
};

// Fills the required create-mode fields: name, an Aegis Cloud storage (which
// sets the URI and the storage key variables), and the repository password.
const fillCreateForm = async (
  user: ReturnType<typeof renderWithProviders>["user"],
  { id, password }: { id: string; password: string },
) => {
  const nameInput = await screen.findByPlaceholderText("repo1");
  await user.type(nameInput, id);
  const storage = await screen.findByTestId("add-repo-aegis-storage");
  await waitFor(() => expect(storage).toBeEnabled());
  await user.click(storage);
  await user.click(await screen.findByRole("option", { name: /Asia primary/ }));
  await waitFor(() => expect(getUriInput()).toHaveValue(AEGIS_URI));
  await user.type(getPasswordInput(), password);
};

describe("AddRepoModal", () => {
  beforeEach(() => {
    // pathAutocomplete is called by the URIAutocomplete on every keystroke; it
    // must return a thenable StringList or the input handler throws.
    vi.mocked(backrestService.pathAutocomplete).mockResolvedValue(
      create(StringListSchema, { values: [] }),
    );
    vi.mocked(backrestService.listSnapshots).mockResolvedValue(
      create(ResticSnapshotListSchema, {}),
    );
    vi.mocked(authenticatedFetch).mockImplementation(
      async () => new Response(JSON.stringify(aegisStatus)),
    );
  });

  it("renders name, uri, password fields and the submit button in create mode", async () => {
    renderWithProviders(<AddRepoModal template={null} />, {
      config: makeConfig(),
    });

    expect(await screen.findByPlaceholderText("repo1")).toBeInTheDocument();
    expect(getUriInput()).toBeInTheDocument();
    expect(getPasswordInput()).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    ).toBeInTheDocument();
  });

  it("blocks submission and surfaces validation feedback for an invalid repo name", async () => {
    const errorSpy = vi.spyOn(alerts, "error");
    const { user } = renderWithProviders(<AddRepoModal template={null} />, {
      config: makeConfig(),
    });

    // "bad name" contains a space, which fails namePattern.
    const nameInput = await screen.findByPlaceholderText("repo1");
    await user.type(nameInput, "bad name");

    // Inline validation feedback becomes visible.
    expect(
      await screen.findByText(m.settings_auth_name_pattern()),
    ).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    );

    expect(backrestService.addRepo).not.toHaveBeenCalled();
    expect(errorSpy).toHaveBeenCalled();
  });

  it("creates a repo: calls addRepo, updates config context, and shows a success toast", async () => {
    const successSpy = vi.spyOn(alerts, "success");
    const resolvedConfig = makeConfig({
      repos: [makeRepo({ id: "myrepo", uri: AEGIS_URI })],
    });
    vi.mocked(backrestService.addRepo).mockResolvedValue(resolvedConfig);

    const { user, setConfig } = renderWithProviders(
      <AddRepoModal template={null} />,
      { config: makeConfig() },
    );

    await fillCreateForm(user, {
      id: "myrepo",
      password: "supersecret",
    });

    await user.click(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    );

    await waitFor(() => expect(backrestService.addRepo).toHaveBeenCalled());

    // The submit path (handleOk) writes directly via addRepo; checkRepoExists
    // is only exercised by the separate "Test Configuration" button.
    expect(backrestService.checkRepoExists).not.toHaveBeenCalled();
    expect(backrestService.addRepo).toHaveBeenCalledWith(
      expect.objectContaining({
        repo: expect.objectContaining({
          id: "myrepo",
          uri: AEGIS_URI,
          password: "supersecret",
          env: aegisStorageEnv,
        }),
      }),
    );
    expect(setConfig).toHaveBeenCalledWith(resolvedConfig);
    expect(successSpy).toHaveBeenCalledWith(
      m.add_repo_modal_success_added({ uri: AEGIS_URI }),
    );
  });

  it("Test Configuration on an existing repo calls checkRepoExists and reports it exists", async () => {
    const successSpy = vi.spyOn(alerts, "success");
    vi.mocked(backrestService.checkRepoExists).mockResolvedValue(
      create(CheckRepoExistsResponseSchema, { exists: true }),
    );

    const { user } = renderWithProviders(<AddRepoModal template={null} />, {
      config: makeConfig(),
    });

    await fillCreateForm(user, {
      id: "myrepo",
      password: "supersecret",
    });

    await user.click(
      screen.getByRole("button", { name: m.add_repo_modal_test_config() }),
    );

    await waitFor(() =>
      expect(backrestService.checkRepoExists).toHaveBeenCalled(),
    );
    expect(backrestService.checkRepoExists).toHaveBeenCalledWith(
      expect.objectContaining({
        repo: expect.objectContaining({ id: "myrepo", uri: AEGIS_URI }),
      }),
    );
    expect(backrestService.addRepo).not.toHaveBeenCalled();
    expect(successSpy).toHaveBeenCalledWith(
      m.add_repo_modal_test_success_existing({ uri: AEGIS_URI }),
    );
  });

  it("refuses a repository that is not on an Aegis Cloud storage", async () => {
    const errorSpy = vi.spyOn(alerts, "error");
    const { user } = renderWithProviders(<AddRepoModal template={null} />, {
      config: makeConfig(),
    });

    await user.type(await screen.findByPlaceholderText("repo1"), "local");
    await user.click(getUriInput());
    await user.paste("/tmp/repo");
    await user.type(getPasswordInput(), "supersecret");
    await user.click(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    );

    await waitFor(() => expect(errorSpy).toHaveBeenCalled());
    const [content] = errorSpy.mock.calls[errorSpy.mock.calls.length - 1];
    expect(String(content)).toContain(m.aegis_repo_uri_required());
    expect(backrestService.addRepo).not.toHaveBeenCalled();
  });

  it("edit mode prefills and disables identity fields and deletes via confirm", async () => {
    const template = makeRepo({
      id: "existing-repo",
      uri: "/tmp/existing",
      password: "existing-pw",
    });
    const resolvedConfig = makeConfig();
    vi.mocked(backrestService.removeRepo).mockResolvedValue(resolvedConfig);

    const { user, setConfig } = renderWithProviders(
      <AddRepoModal template={template} />,
      { config: makeConfig({ repos: [template] }) },
    );

    // Prefilled + disabled identity/connection fields.
    const nameInput = (await screen.findByDisplayValue(
      "existing-repo",
    )) as HTMLInputElement;
    expect(nameInput).toBeDisabled();
    expect(getUriInput()).toHaveValue("/tmp/existing");
    expect(getUriInput()).toBeDisabled();
    expect(getPasswordInput()).toBeDisabled();

    // Delete requires a confirm click (ConfirmButton second press).
    await user.click(
      screen.getByRole("button", { name: m.add_plan_modal_button_delete() }),
    );
    const confirm = await screen.findByRole("button", {
      name: m.add_plan_modal_button_confirm_delete(),
    });
    await user.click(confirm);

    await waitFor(() => expect(backrestService.removeRepo).toHaveBeenCalled());
    expect(backrestService.removeRepo).toHaveBeenCalledWith(
      expect.objectContaining({ repoId: "existing-repo" }),
    );
    expect(setConfig).toHaveBeenCalledWith(resolvedConfig);
  });

  it("uses onSaveOverride when provided and does not call addRepo", async () => {
    const successSpy = vi.spyOn(alerts, "success");
    const onSaveOverride = vi.fn().mockResolvedValue(undefined);

    const { user } = renderWithProviders(
      <AddRepoModal template={null} onSaveOverride={onSaveOverride} />,
      { config: makeConfig() },
    );

    await fillCreateForm(user, {
      id: "myrepo",
      password: "supersecret",
    });

    await user.click(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    );

    await waitFor(() => expect(onSaveOverride).toHaveBeenCalled());
    expect(onSaveOverride).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "myrepo",
        uri: AEGIS_URI,
        password: "supersecret",
      }),
    );
    expect(backrestService.addRepo).not.toHaveBeenCalled();
    expect(successSpy).toHaveBeenCalledWith(
      m.add_repo_modal_success_updated({ uri: AEGIS_URI }),
    );
  });

  it("shows an error toast and keeps the modal open when addRepo rejects", async () => {
    const errorSpy = vi.spyOn(alerts, "error");
    vi.mocked(backrestService.addRepo).mockRejectedValue(
      connectError(Code.Internal, "init failed"),
    );

    const { user } = renderWithProviders(<AddRepoModal template={null} />, {
      config: makeConfig(),
    });

    await fillCreateForm(user, {
      id: "myrepo",
      password: "supersecret",
    });

    const submit = screen.getByRole("button", {
      name: m.add_plan_modal_button_submit(),
    });
    await user.click(submit);

    await waitFor(() => expect(errorSpy).toHaveBeenCalled());
    const [content] = errorSpy.mock.calls[errorSpy.mock.calls.length - 1];
    expect(String(content)).toContain("init failed");

    // Modal is still mounted and the submit button is interactive again.
    expect(
      screen.getByRole("button", { name: m.add_plan_modal_button_submit() }),
    ).not.toBeDisabled();
  });
});
