import { useEffect, useMemo, useRef, useState } from "react";
import { useBlocker, useNavigate, useLocation } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Notice, FolderPicker, Icon } from "@panasms/ui";
import { managed } from "@panasms/operations";
import { request } from "@panasms/client";
import { translator } from "@panasms/i18n";
import { mdiArrowLeft, mdiFolderOpenOutline, mdiPlay, mdiCheck } from "@mdi/js";
import { Modal } from "./container-dialog";
import { Builder } from "./compose-builder";
import { ComposeCode } from "./compose-code";
import { parseCompose } from "./compose-model";
const tr = translator("containers");
type Draft = {
  source: string;
  env: string;
  directory: string;
  revision: string;
};
type Plan = {
  images: { reference: string; status: string; id?: string }[];
  token: string;
  services: {
    name: string;
    image: string;
    ports: string[] | null;
    mounts: string[] | null;
    change: string;
  }[];
};
const initial: Draft = {
  source: "services:\n  web:\n    image: nginx:alpine\n",
  env: "",
  directory: "",
  revision: "",
};
const api = <T,>(path: string, body?: unknown) =>
  request<T>("module-api/containers/" + path, body ? "POST" : "GET", body);
export function ComposeEditor({
  name,
  ready,
}: {
  name?: string;
  ready: boolean;
}) {
  const q = useQuery({
    queryKey: ["containers", "editor", name],
    queryFn: () => api<Draft>("project?id=" + encodeURIComponent(name!)),
    enabled: !!name,
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  if (name && (q.isPending || (q.isFetching && !q.data)))
    return <p role="status">{tr("loading")}</p>;
  if (name && q.error) return <Notice error>{q.error.message}</Notice>;
  return (
    <Editor name={name} original={name ? q.data! : initial} ready={ready} />
  );
}
type FileEntry = {
  name: string;
  path: string;
  size: number;
  directory: boolean;
  link: boolean;
};
async function readFile(e: FileEntry, limit: number) {
  if (e.size > limit) throw Error(tr("composeTooLarge"));
  const r = await fetch(
    "/api/v1/files/content?" +
      new URLSearchParams({ target: "viewer:" + e.path }),
  );
  if (!r.ok) throw Error(tr("composeReadError"));
  const text = await r.text();
  if (new TextEncoder().encode(text).length > limit)
    throw Error(tr("composeTooLarge"));
  return text;
}
function ImportFile({
  accept,
  close,
}: {
  accept: (s: string, env: string, dir: string) => void;
  close: () => void;
}) {
  const [folder, setFolder] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const q = useQuery({
    queryKey: ["containers", "compose-input", folder],
    queryFn: () =>
      managed<{ entries: FileEntry[] }>("files", undefined, folder),
    enabled: !!folder,
  });
  const files = q.data?.entries.filter((e) => !e.directory && !e.link) ?? [];
  async function choose(e: FileEntry) {
    setBusy(true);
    try {
      const env = files.find((f) => f.name === ".env");
      accept(
        await readFile(e, 256 << 10),
        env ? await readFile(env, 64 << 10) : "",
        folder,
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="containers-form">
      <div className="compose-line">
        <strong>{tr("composeImport")}</strong>
        <Button disabled={busy} onClick={close}>
          {tr("cancel")}
        </Button>
      </div>
      {error && <Notice error>{error}</Notice>}
      {!folder ? (
        <FolderPicker policy="share" onChoose={setFolder} />
      ) : (
        <>
          <Button disabled={busy} onClick={() => setFolder("")}>
            {folder}
          </Button>
          {q.isPending ? (
            <p>{tr("loading")}</p>
          ) : q.error ? (
            <Notice error>{q.error.message}</Notice>
          ) : (
            <>
              {files
                .filter((e) => /\.ya?ml$/i.test(e.name))
                .map((e) => (
                  <Button
                    disabled={busy}
                    key={e.path}
                    onClick={() => void choose(e)}
                  >
                    {e.name}
                  </Button>
                ))}
              {!files.some((e) => /\.ya?ml$/i.test(e.name)) && (
                <p>{tr("composeNoFiles")}</p>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}
function Editor({
  name,
  original,
  ready,
}: {
  name?: string;
  original: Draft;
  ready: boolean;
}) {
  const location = useLocation();
  const navigate = useNavigate(),
    cache = useQueryClient();
  const [draft, setDraft] = useState(original),
    [target, setTarget] = useState(name ?? ""),
    [mode, setMode] = useState(name ? "yaml" : "form"),
    [importing, setImport] = useState(false);
  const [plan, setPlan] = useState<Plan>(),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [job, setJob] = useState(
      () => (name && new URLSearchParams(location.search).get("job")) || "",
    );
  const op = useRef(""),
    allow = useRef(false);
  const parsed = useMemo(() => {
    try {
      return { doc: parseCompose(draft.source) };
    } catch (e) {
      return { error: (e as Error).message };
    }
  }, [draft.source]);
  const dirty =
    target !== (name ?? "") ||
    draft.source !== original.source ||
    draft.env !== original.env ||
    draft.directory !== original.directory;
  const blocker = useBlocker(() => dirty && !allow.current);
  useEffect(() => {
    if (!dirty) return;
    const leave = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [dirty]);
  function reset() {
    setPlan(undefined);
    setError("");
    op.current = "";
  }
  function edit(v: Partial<Draft>) {
    reset();
    setDraft((d) => ({ ...d, ...v }));
  }
  const body = {
    action: "project.apply",
    target,
    compose: draft.source,
    env: draft.env,
    directory: draft.directory,
    revision: draft.revision,
  };
  async function check() {
    setBusy(true);
    reset();
    try {
      setPlan(await api<Plan>("project/check", body));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const missing =
    plan?.images.filter((i) => i.status === "missing").length ?? 0;
  const unavailable = plan?.images.some(
    (i) => !["missing", "compatible"].includes(i.status),
  );
  async function apply() {
    if (!plan || busy) return;
    setBusy(true);
    setError("");
    if (!op.current)
      op.current = Array.from(crypto.getRandomValues(new Uint8Array(16)), (v) =>
        v.toString(16).padStart(2, "0"),
      ).join("");
    try {
      await api("action", {
        ...body,
        action: "project.apply",
        plan: plan.token,
        id: op.current,
      });
      setJob(op.current);
      void cache.invalidateQueries({ queryKey: ["containers", "jobs"] });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const jobs = useQuery({
    queryKey: ["containers", "compose-job", job],
    queryFn: () =>
      api<
        {
          id: string;
          action: string;
          target: string;
          status: string;
          error?: string;
        }[]
      >("jobs"),
    enabled: !!job,
    refetchInterval: job ? 1500 : false,
  });
  const result = jobs.data?.find((j) => j.id === job && j.target === target);
  useEffect(() => {
    if (!job) return;
    if (!result) {
      if (jobs.data && !jobs.isFetching && jobs.isFetchedAfterMount) {
        setJob("");
        setError(tr("composeJobGone"));
      }
      return;
    }
    if (result.status === "succeeded") {
      allow.current = true;
      void cache.invalidateQueries({ queryKey: ["containers"] });
      navigate("/containers/projects/" + target, { replace: true });
    } else if (["failed", "cancelled", "interrupted"].includes(result.status)) {
      setError(result.error || tr("failed"));
      setJob("");
      setPlan(undefined);
      op.current = "";
      void api<Draft>("project?id=" + encodeURIComponent(target))
        .then((current) => {
          if (
            current.source === draft.source &&
            current.env === draft.env &&
            current.directory === draft.directory
          )
            setDraft((d) => ({ ...d, revision: current.revision }));
        })
        .catch(() => {});
    }
  }, [result?.status, job, jobs.data, jobs.isFetching]);
  return (
    <section className="compose-editor">
      <div className="compose-line">
        <Button
          title={tr("projects")}
          onClick={() =>
            navigate(
              name ? "/containers/projects/" + name : "/containers/projects",
            )
          }
        >
          <Icon path={mdiArrowLeft} />
        </Button>
        <h2>{name || tr("composeNew")}</h2>
      </div>
      {blocker.state === "blocked" && (
        <Modal
          compact
          title={tr("discardTitle")}
          close={() => blocker.reset()}
          footer={
            <>
              <Button data-dialog-cancel onClick={() => blocker.reset()}>
                {tr("cancel")}
              </Button>
              <Button className="danger" onClick={() => blocker.proceed()}>
                {tr("discard")}
              </Button>
            </>
          }
        >
          <p>{tr("discardHint")}</p>
        </Modal>
      )}
      <fieldset disabled={busy || !!job} className="compose-fields">
        <label>
          {tr("name")}
          <input
            value={target}
            disabled={!!name}
            placeholder="my-project"
            onChange={(e) => {
              reset();
              setTarget(e.target.value);
            }}
          />
        </label>
        <div className="compose-line">
          <div
            className="compose-switch"
            role="group"
            aria-label={tr("composeMode")}
          >
            {["form", "yaml"].map((value) => (
              <Button
                key={value}
                aria-pressed={mode === value}
                onClick={() => {
                  if (value === "form" && !parsed.doc) {
                    setError(tr("composeFixYaml") + " " + parsed.error);
                    return;
                  }
                  setMode(value);
                }}
              >
                {value === "form" ? tr("composeBuilder") : "YAML"}
              </Button>
            ))}
          </div>
          <Button
            title={tr("composeImport")}
            onClick={() => setImport(!importing)}
          >
            <Icon path={mdiFolderOpenOutline} />
          </Button>
        </div>
        {importing ? (
          <ImportFile
            close={() => setImport(false)}
            accept={(source, env, directory) => {
              edit({ source, env, directory });
              if (!target)
                setTarget(
                  directory
                    .split("/")
                    .pop()
                    ?.toLowerCase()
                    .replace(/[^a-z0-9_-]/g, "-") ?? "",
                );
              setMode("yaml");
              setImport(false);
            }}
          />
        ) : mode === "form" && parsed.doc ? (
          <Builder
            text={draft.source}
            parsed={parsed.doc}
            change={(source) => edit({ source })}
            code={() => setMode("yaml")}
          />
        ) : (
          <div inert={busy || !!job}>
            <ComposeCode
              text={draft.source}
              change={(source) => edit({ source })}
            />
            {parsed.error && (
              <Notice error>
                {tr("composeFixYaml")} {parsed.error}
              </Notice>
            )}
          </div>
        )}
        <details>
          <summary>{tr("composeContext")}</summary>
          <label>
            {tr("composeDirectory")}
            <input
              value={draft.directory}
              onChange={(e) => edit({ directory: e.target.value })}
            />
          </label>
          <details>
            <summary>{tr("composeEnv")}</summary>
            <textarea
              aria-label=".env"
              spellCheck={false}
              value={draft.env}
              onChange={(e) => edit({ env: e.target.value })}
            />
          </details>
        </details>
      </fieldset>
      {error && <Notice error>{error}</Notice>}
      {jobs.error && <Notice error>{jobs.error.message}</Notice>}
      {plan && (
        <section className="compose-plan">
          <h3>{tr("composePlan")}</h3>
          <p>{tr("composeLocalOnly")}</p>
          <ul>
            {plan.images.map((i) => (
              <li key={i.reference}>
                <strong>{i.reference}</strong> ·{" "}
                {tr("composeImage." + i.status)}
              </li>
            ))}
          </ul>
          {plan.services.map((s) => (
            <div key={s.name}>
              <strong>{s.name}</strong> · {tr("plan." + s.change)}{" "}
              <span>{s.image}</span>
              {[...(s.ports ?? []), ...(s.mounts ?? [])].map((v, i) => (
                <small key={i}>{v}</small>
              ))}
            </div>
          ))}
        </section>
      )}
      <div className="compose-line">
        {job ? (
          <p role="status">{tr("composeRunning")}</p>
        ) : (
          <>
            {!plan ? (
              <Button
                className="primary"
                disabled={!ready || busy || !target || !parsed.doc || importing}
                onClick={() => void check()}
              >
                <Icon path={mdiCheck} />
                {tr(busy ? "checking" : "composeCheck")}
              </Button>
            ) : (
              <Button
                className="primary"
                disabled={!ready || busy || unavailable}
                onClick={() => void apply()}
              >
                <Icon path={mdiPlay} />
                {missing
                  ? tr("composeFetchLaunch", { count: String(missing) })
                  : tr(name ? "composeApply" : "composeLaunch")}
              </Button>
            )}
          </>
        )}
      </div>
    </section>
  );
}
